package nntp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
)

// ExecuteWithFailover retries transient failures and changes providers.
// Article failures exclude their backbone for the rest of the operation.
func (c *Client) ExecuteWithFailover(ctx context.Context, workload Workload, fn func(conn *Connection) error) error {
	if !workload.valid() {
		return fmt.Errorf("invalid NNTP workload: %s", workload)
	}
	var lastErr error
	var exclusions providerExclusions
	var previousHost string
	var attempts map[string]int
	perProvider := max(1, c.retries+1)
	for range len(c.providers) * perProvider {
		if err := ctx.Err(); err != nil {
			return err
		}
		selection := exclusions
		if previousHost != "" {
			alternate := providerExclusions{hosts: maps.Clone(exclusions.hosts), backbones: exclusions.backbones}
			alternate.excludeHost(previousHost)
			if c.hasEligibleProviderInTier(alternate, false) || c.hasEligibleProviderInTier(alternate, true) {
				selection = alternate
			}
		}
		conn, provider, err := c.getAnyAvailableConnection(ctx, workload, selection)
		if err != nil && previousHost != "" && ctx.Err() == nil {
			// Keep article and exhausted-provider exclusions when alternatives fail.
			conn, provider, err = c.getAnyAvailableConnection(ctx, workload, exclusions)
		}
		if err != nil {
			lastErr = err
			break
		}
		err = c.safeExecute(conn, fn)
		transient, excludeArticle, discard := false, false, false
		if typed, ok := errors.AsType[*Error](err); ok {
			switch typed.Type {
			case ErrorTypeArticleNotFound, ErrorTypeYencDecode:
				excludeArticle = true
			case ErrorTypeConnection, ErrorTypeTimeout, ErrorTypeServerBusy:
				transient, discard = true, true
			}
		} else if customerror.IsPanicError(err) {
			discard = true
		}
		if discard {
			c.release(conn)
		} else {
			c.returnOrReleaseConn(conn, provider)
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempts == nil {
			attempts = make(map[string]int)
		}
		providerID := provider.ID()
		attempts[providerID]++
		lastErr = err
		previousHost = ""
		switch {
		case excludeArticle:
			excludeForArticleNotFound(&exclusions, provider)
		case transient:
			previousHost = provider.Host
			if attempts[providerID] >= perProvider {
				exclusions.excludeHost(provider.Host)
			}
			if !c.hasEligibleProviderInTier(exclusions, false) && !c.hasEligibleProviderInTier(exclusions, true) {
				break
			}
			delay := config.DefaultRetryDelay
			for i := 1; i < attempts[providerID] && delay < config.DefaultRetryDelayMax; i++ {
				delay = min(delay*2, config.DefaultRetryDelayMax)
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		case discard:
			exclusions.excludeHost(provider.Host)
		default:
			return err
		}
		if !c.hasEligibleProviderInTier(exclusions, false) && !c.hasEligibleProviderInTier(exclusions, true) {
			break
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w: %w", ErrAllProvidersFailed, lastErr)
	}
	return ErrAllProvidersFailed
}
