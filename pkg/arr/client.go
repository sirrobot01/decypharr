package arr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	json "github.com/bytedance/sonic"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
)

var (
	ErrNotConfigured   = errors.New("arr is not configured")
	ErrUnsupportedType = errors.New("unsupported arr type")
)

// transportError is a request that never produced a response.
type transportError struct{ cause error }

func (e *transportError) Error() string { return e.cause.Error() }
func (e *transportError) Unwrap() error { return e.cause }

type responseDecoder func(*http.Response) error

func decodeResponseInto(out any) responseDecoder {
	if out == nil {
		return nil
	}
	return func(resp *http.Response) error {
		return request.DecodeJSON(resp, out)
	}
}

// get issues a read. Reads are retried by the shared client.
func (s *Service) get(ctx context.Context, instance Arr, endpoint string, out any) (*http.Response, error) {
	return s.getDecoded(ctx, instance, endpoint, decodeResponseInto(out))
}

// getDecoded issues a read whose successful response has a specialized
// decoder. Large collection endpoints use it to process one element at a time.
func (s *Service) getDecoded(ctx context.Context, instance Arr, endpoint string, decode responseDecoder) (*http.Response, error) {
	return s.do(ctx, s.client, instance, http.MethodGet, endpoint, nil, decode)
}

// mutate issues a write. Writes are never retried, and a request that may have
// reached the Arr is reported through ErrMutationOutcomeUnknown.
func (s *Service) mutate(ctx context.Context, instance Arr, method, endpoint string, payload, out any) (*http.Response, error) {
	return s.do(ctx, s.mutation, instance, method, endpoint, payload, decodeResponseInto(out))
}

func (s *Service) do(
	ctx context.Context,
	client *request.Client,
	instance Arr,
	method, endpoint string,
	payload any,
	decode responseDecoder,
) (*http.Response, error) {
	if !instance.Reachable() {
		return nil, fmt.Errorf("%w: %q", ErrNotConfigured, instance.Name)
	}
	url, err := utils.JoinURL(instance.Host, endpoint)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", instance.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, &transportError{cause: err}
	}
	// Callers only read status and headers, which stay valid after close, so
	// the body lifecycle is owned here.
	defer request.DrainAndCloseResponse(resp)

	if decode != nil && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		if err := decode(resp); err != nil && !errors.Is(err, io.EOF) {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp, nil
}

// dispatched reports whether a failed mutation may still have reached the Arr.
func dispatched(resp *http.Response, err error) bool {
	if resp != nil {
		return true
	}
	_, ok := errors.AsType[*transportError](err)
	return ok
}

func expectStatus(resp *http.Response, allowed ...int) error {
	if resp == nil {
		return errors.New("arr returned no response")
	}
	if slices.Contains(allowed, resp.StatusCode) {
		return nil
	}
	return fmt.Errorf("arr returned %s", resp.Status)
}

func expectSuccess(resp *http.Response) error {
	if resp == nil {
		return errors.New("arr returned no response")
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	return fmt.Errorf("arr returned %s", resp.Status)
}

// Probe reports which application an instance runs, and fails when it does not
// answer. It works on an instance that is not registered yet.
func (s *Service) Probe(ctx context.Context, instance Arr) (Type, error) {
	if !instance.Reachable() {
		return Others, fmt.Errorf("%w: %q", ErrNotConfigured, instance.Name)
	}
	if utils.ValidateURL(instance.Host) != nil {
		return Others, fmt.Errorf("%w: invalid host %q", ErrNotConfigured, instance.Host)
	}

	var status struct {
		AppName string `json:"appName"`
	}
	resp, err := s.get(ctx, instance, "api/v3/system/status", &status)
	if err != nil {
		return Others, err
	}
	// Lidarr and Readarr serve v1, so a 404 still means the instance answered.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return Others, fmt.Errorf("validate arr %s: %s", instance.Name, resp.Status)
	}
	if kind := typeFromAppName(status.AppName); kind != Others {
		return kind, nil
	}
	return inferType(instance.Host, instance.Name), nil
}

// ErrMutationOutcomeUnknown marks a mutation whose result the Arr never
// confirmed. The caller must reconcile against the Arr before retrying, or it
// risks blocklisting or searching the same release twice.
var ErrMutationOutcomeUnknown = errors.New("Arr mutation outcome is unknown")

type mutationOutcomeUnknownError struct {
	cause      error
	retryAfter time.Duration
}

func (err *mutationOutcomeUnknownError) Error() string {
	return fmt.Sprintf("%s: %v", ErrMutationOutcomeUnknown, err.cause)
}

func (err *mutationOutcomeUnknownError) Unwrap() error {
	return errors.Join(ErrMutationOutcomeUnknown, err.cause)
}

// UnknownMutationOutcome wraps cause as an unconfirmed mutation. retryAfter is
// how long the caller should wait before the Arr is expected to show it.
func UnknownMutationOutcome(cause error, retryAfter time.Duration) error {
	if cause == nil {
		cause = errors.New("remote mutation was not visible during reconciliation")
	}
	return &mutationOutcomeUnknownError{cause: cause, retryAfter: retryAfter}
}

// MutationRetryAfter returns the wait an unconfirmed mutation asked for.
func MutationRetryAfter(err error) time.Duration {
	unknown, ok := errors.AsType[*mutationOutcomeUnknownError](err)
	if !ok {
		return 0
	}
	return unknown.retryAfter
}
