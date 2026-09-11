package repair

import (
	"context"
	"errors"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
)

type nzbProbeClient interface {
	CheckFile(context.Context, string, string) error
	VerifyFile(context.Context, string, string) error
}

type nzbProbeRequest struct {
	nzbID         string
	fileName      string
	verifyContent bool
}

type nzbProber struct {
	client nzbProbeClient
}

func newNZBProber(client nzbProbeClient) *nzbProber {
	return &nzbProber{client: client}
}

func (p *nzbProber) probe(ctx context.Context, request nzbProbeRequest) fileResult {
	result := fileResult{
		name:     request.fileName,
		infoHash: request.nzbID,
		protocol: config.ProtocolNZB,
	}
	if p == nil || p.client == nil {
		result.reason = "usenet_client_not_configured"
		return result
	}

	probeErr := p.client.CheckFile(ctx, request.nzbID, request.fileName)
	if probeErr == nil && request.verifyContent {
		probeErr = p.client.VerifyFile(ctx, request.nzbID, request.fileName)
	}
	if probeErr == nil {
		result.healthy = true
		return result
	}

	switch {
	case errors.Is(probeErr, customerror.UsenetSegmentMissingError):
		result.broken = true
		result.reason = "usenet_segment_missing"
	case errors.Is(probeErr, customerror.UsenetCorruptContentError):
		result.broken = true
		result.reason = "usenet_corrupt_content"
	default:
		result.deferred = true
		result.reason = "usenet_probe_error"
	}
	return result
}
