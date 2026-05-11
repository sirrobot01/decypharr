package request

import (
	"context"
	"crypto/tls"
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

// WithRateLimiter sets a rate limiter
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

// Do performs an HTTP request with retries for certain status codes
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Apply headers
	c.headersMu.RLock()
	if c.headers != nil {
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
	}
	c.headersMu.RUnlock()

	// Apply rate limiting
	if c.rateLimiter != nil {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		default:
			c.rateLimiter.Take()
		}
	}

	// Convert to retryablehttp request
	retryReq, err := retryablehttp.FromRequest(req)
	if err != nil {
		return nil, fmt.Errorf("creating retryable request: %w", err)
	}

	return c.client.Do(retryReq)
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
	retryClient.Logger = nil // Disable default logging

	// Custom retry policy based on retryable status codes
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		// Don't retry on context errors
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		// First use the default retry policy for error handling
		// This handles the case when resp is nil (network errors)
		shouldRetry, defaultErr := retryablehttp.DefaultRetryPolicy(ctx, resp, err)
		if defaultErr != nil {
			return false, defaultErr
		}
		if shouldRetry {
			return true, nil
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
