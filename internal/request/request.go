package request

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"go.uber.org/ratelimit"
	"golang.org/x/net/proxy"
)

var (
	once     sync.Once
	instance *Client
)

type ClientOption func(*Client)

// Client represents an HTTP client with additional capabilities
type Client struct {
	client          *retryablehttp.Client
	httpClient      *http.Client // underlying http client
	rateLimiter     ratelimit.Limiter
	throttle        *Throttle
	headers         map[string]string
	headersMu       sync.RWMutex
	maxRetries      int
	timeout         time.Duration
	skipTLSVerify   bool
	retryableStatus map[int]struct{}
	logger          zerolog.Logger
	proxy           string
}

// WithMaxRetries sets the maximum number of retry attempts
func WithMaxRetries(maxRetries int) ClientOption {
	return func(c *Client) {
		c.maxRetries = maxRetries
	}
}

// WithTimeout sets the request timeout
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = timeout
	}
}

// WithThrottle shares a cooldown with the other clients of one API key.
func WithThrottle(throttle *Throttle) ClientOption {
	return func(c *Client) {
		c.throttle = throttle
	}
}

// WithRateLimiter sets the limiter that every attempt of a request passes.
func WithRateLimiter(rl ratelimit.Limiter) ClientOption {
	return func(c *Client) {
		c.rateLimiter = rl
	}
}

// WithHeaders sets default headers
func WithHeaders(headers map[string]string) ClientOption {
	return func(c *Client) {
		c.headersMu.Lock()
		c.headers = headers
		c.headersMu.Unlock()
	}
}

func (c *Client) SetHeader(key, value string) {
	c.headersMu.Lock()
	c.headers[key] = value
	c.headersMu.Unlock()
}

func WithLogger(logger zerolog.Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

func WithTransport(transport *http.Transport) ClientOption {
	return func(c *Client) {
		c.httpClient.Transport = transport
	}
}

// WithRetryableStatus adds status codes that should trigger a retry
func WithRetryableStatus(statusCodes ...int) ClientOption {
	return func(c *Client) {
		c.retryableStatus = make(map[int]struct{}) // reset the map
		for _, code := range statusCodes {
			c.retryableStatus[code] = struct{}{}
		}
	}
}

func WithProxy(proxyURL string) ClientOption {
	return func(c *Client) {
		c.proxy = proxyURL
	}
}

// prepare applies headers and checks the cooldown and rate limit.
func (c *Client) prepare(req *http.Request) error {
	c.headersMu.RLock()
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	c.headersMu.RUnlock()

	if wait := c.throttle.Remaining(); wait > 0 {
		return &ThrottledError{RetryAfter: wait}
	}

	if c.rateLimiter != nil {
		select {
		case <-req.Context().Done():
			return req.Context().Err()
		default:
			c.rateLimiter.Take()
		}
	}
	return nil
}

// Do performs an HTTP request with retries for certain status codes
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := c.prepare(req); err != nil {
		return nil, err
	}

	// Convert to retryablehttp request
	retryReq, err := retryablehttp.FromRequest(req)
	if err != nil {
		return nil, fmt.Errorf("creating retryable request: %w", err)
	}

	return c.client.Do(retryReq)
}

// DoOnce sends req exactly once and returns the response whatever its status.
// Use it for an endpoint with a tight limit of its own, where a retry spends
// that limit. The caller closes the body and owns any reaction to a 429.
func (c *Client) DoOnce(req *http.Request) (*http.Response, error) {
	if err := c.prepare(req); err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

// MakeRequest performs an HTTP request and returns the response body as bytes
func (c *Client) MakeRequest(req *http.Request) ([]byte, error) {
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err := res.Body.Close(); err != nil {
			c.logger.Printf("Failed to close response body: %v", err)
		}
	}()

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP error %d: %s", res.StatusCode, string(bodyBytes))
	}

	return bodyBytes, nil
}

func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}

	return c.Do(req)
}

// retryAfterBackoff extends DefaultBackoff with Retry-After header support.
// When a 429 response carries a Retry-After header decypharr waits exactly as
// long as the server requests instead of using jittered exponential backoff.
func retryAfterBackoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		if wait := RetryAfter(resp); wait > 0 {
			if wait > max {
				return max
			}
			return wait
		}
	}
	return retryablehttp.DefaultBackoff(min, max, attemptNum, resp)
}

// handleExhausted runs when a request will not be tried again. A 429 keeps its
// type here, because callers cannot see the status of a request that failed.
func (c *Client) handleExhausted(resp *http.Response, err error, tries int) (*http.Response, error) {
	if resp != nil {
		defer DrainAndClose(resp.Body)
	}
	if throttled, ok := errors.AsType[*ThrottledError](err); ok {
		return nil, throttled
	}
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		return nil, &ThrottledError{RetryAfter: RetryAfter(resp)}
	}
	if err != nil {
		return nil, fmt.Errorf("giving up after %d attempt(s): %w", tries, err)
	}
	if resp != nil && resp.Request != nil {
		return nil, fmt.Errorf("%s %s giving up after %d attempt(s), status %d", resp.Request.Method, resp.Request.URL.Path, tries, resp.StatusCode)
	}
	return nil, fmt.Errorf("giving up after %d attempt(s)", tries)
}

// New creates a new HTTP client with the specified options
func New(options ...ClientOption) *Client {
	client := &Client{
		maxRetries:    5,
		skipTLSVerify: true,
		retryableStatus: map[int]struct{}{
			http.StatusTooManyRequests:     {},
			http.StatusInternalServerError: {},
			http.StatusBadGateway:          {},
			http.StatusServiceUnavailable:  {},
			http.StatusGatewayTimeout:      {},
		},
		logger:  logger.New("request"),
		timeout: 60 * time.Second,
		proxy:   "",
		headers: make(map[string]string),
	}

	// Create default http client
	client.httpClient = &http.Client{
		Timeout: client.timeout,
	}

	// Apply options before configuring transport
	for _, option := range options {
		option(client)
	}

	client.httpClient.Timeout = client.timeout

	// Check if transport was set by WithTransport option
	if client.httpClient.Transport == nil {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: client.skipTLSVerify,
			},
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 15 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		}

		// Configure proxy if needed
		SetProxy(transport, client.proxy)

		// Set the transport to the client
		client.httpClient.Transport = transport
	}

	// Create retryablehttp client
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = client.httpClient
	retryClient.RetryMax = client.maxRetries
	retryClient.RetryWaitMin = 1 * time.Second
	retryClient.RetryWaitMax = 30 * time.Second
	retryClient.Logger = nil
	retryClient.Backoff = retryAfterBackoff
	retryClient.ErrorHandler = client.handleExhausted
	// Do charges the limiter for the first attempt. Retries must pay as well,
	// or they spend the provider budget unmetered.
	retryClient.RequestLogHook = func(_ retryablehttp.Logger, _ *http.Request, attempt int) {
		if attempt > 0 && client.rateLimiter != nil {
			client.rateLimiter.Take()
		}
	}

	// Custom retry policy based on retryable status codes
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		// Don't retry on context errors
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		// Use the default policy for transport errors. HTTP responses use the
		// configured status list so provider errors retain their response body.
		if err != nil || resp == nil {
			return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := RetryAfter(resp)
			if client.throttle.Trip(wait) {
				client.logger.Warn().Dur("retry_after", wait).Str("host", resp.Request.URL.Host).Msg("Provider rate limit reached, pausing requests")
			}
			// A pause longer than one retry wait cannot end inside this
			// request, so sending it again only spends the lockout.
			if wait > retryClient.RetryWaitMax {
				return false, &ThrottledError{RetryAfter: wait}
			}
		}

		// Check for retryable status codes (only if resp is not nil)
		if resp != nil {
			if _, ok := client.retryableStatus[resp.StatusCode]; ok {
				return true, nil
			}
		}

		return false, nil
	}

	client.client = retryClient

	return client
}

func Default() *Client {
	once.Do(func() {
		instance = New()
	})
	return instance
}

func SetProxy(transport *http.Transport, proxyURL string) {
	if proxyURL != "" {
		if strings.HasPrefix(proxyURL, "socks5://") {
			// Handle SOCKS5 proxy
			socksURL, err := url.Parse(proxyURL)
			if err == nil {
				auth := &proxy.Auth{}
				if socksURL.User != nil {
					auth.User = socksURL.User.Username()
					password, _ := socksURL.User.Password()
					auth.Password = password
				}

				dialer, err := proxy.SOCKS5("tcp", socksURL.Host, auth, proxy.Direct)
				if err == nil {
					transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						return dialer.Dial(network, addr)
					}
				}
			}
		} else {
			_proxy, err := url.Parse(proxyURL)
			if err == nil {
				transport.Proxy = http.ProxyURL(_proxy)
			}
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
}
