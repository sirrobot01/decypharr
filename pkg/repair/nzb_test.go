package repair

import (
	"context"
	"errors"
	"testing"

	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type stubNZBRepairClient struct {
	checkFile func(context.Context, string, string) error
	verify    func(context.Context, string, string) error
}

func (c stubNZBRepairClient) CheckFile(ctx context.Context, nzbID, fileName string) error {
	return c.checkFile(ctx, nzbID, fileName)
}

func (c stubNZBRepairClient) VerifyFile(ctx context.Context, nzbID, fileName string) error {
	if c.verify == nil {
		return nil
	}
	return c.verify(ctx, nzbID, fileName)
}

func TestNZBProberReportsHealthyFile(t *testing.T) {
	client := stubNZBRepairClient{checkFile: func(context.Context, string, string) error { return nil }}
	result := newNZBProber(client).probe(t.Context(), nzbProbeRequest{nzbID: "nzb", fileName: "movie.mkv"})
	if !result.healthy || result.broken || result.deferred {
		t.Fatalf("result = %+v", result)
	}
}

func TestNZBProberReportsMissingFile(t *testing.T) {
	client := stubNZBRepairClient{checkFile: func(context.Context, string, string) error {
		return customerror.UsenetSegmentMissingError
	}}
	result := newNZBProber(client).probe(t.Context(), nzbProbeRequest{nzbID: "nzb", fileName: "movie.mkv"})
	if !result.broken || result.reason != "usenet_segment_missing" {
		t.Fatalf("result = %+v", result)
	}
}

func TestNZBProberVerifiesContentWhenRequested(t *testing.T) {
	verified := false
	client := stubNZBRepairClient{
		checkFile: func(context.Context, string, string) error { return nil },
		verify: func(context.Context, string, string) error {
			verified = true
			return customerror.UsenetCorruptContentError
		},
	}
	result := newNZBProber(client).probe(t.Context(), nzbProbeRequest{nzbID: "nzb", fileName: "movie.mkv", verifyContent: true})
	if !verified || !result.broken || result.reason != "usenet_corrupt_content" {
		t.Fatalf("result = %+v, verified = %t", result, verified)
	}
}

func TestNZBProberDefersOperationalFailure(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	client := stubNZBRepairClient{checkFile: func(context.Context, string, string) error { return wantErr }}
	result := newNZBProber(client).probe(t.Context(), nzbProbeRequest{nzbID: "nzb", fileName: "movie.mkv"})
	if result.healthy || result.broken || !result.deferred || result.reason != "usenet_probe_error" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRollupStatusKeepsUnresolvedEntryUnknown(t *testing.T) {
	results := []fileResult{
		{name: "healthy.mkv", healthy: true},
		{name: "unresolved.mkv", deferred: true, reason: "usenet_probe_error"},
	}
	if got := rollupStatus(results); got != storage.HealthUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
	results = append(results, fileResult{name: "broken.mkv", broken: true})
	if got := rollupStatus(results); got != storage.HealthBroken {
		t.Fatalf("status with definitive failure = %q, want broken", got)
	}
}
