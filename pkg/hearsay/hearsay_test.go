package hearsay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/rs/zerolog"

	hearsaylib "github.com/sirrobot01/hearsay"
	hsdebrid "github.com/sirrobot01/hearsay/debrid"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

func TestZerologHandlerDemotesRoutineSyncTraffic(t *testing.T) {
	tests := []struct {
		name    string
		level   slog.Level
		message string
		want    string
	}{
		{name: "published generation", level: slog.LevelInfo, message: "published generation", want: "debug"},
		{name: "ingested generation", level: slog.LevelInfo, message: "ingested generation", want: "debug"},
		{name: "transient fetch failure", level: slog.LevelWarn, message: "fetch failed", want: "trace"},
		{name: "real warning", level: slog.LevelWarn, message: "publish failed", want: "warn"},
		{name: "other information", level: slog.LevelInfo, message: "pruned stale feeds", want: "info"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			log := zerolog.New(&output).Level(zerolog.TraceLevel)
			slog.New(zerologHandler{log: log}).Log(t.Context(), test.level, test.message, "ns", "example")

			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatalf("decode log record: %v", err)
			}
			if got := record[zerolog.LevelFieldName]; got != test.want {
				t.Fatalf("level = %v, want %q", got, test.want)
			}
			if got := record["ns"]; got != "example" {
				t.Fatalf("namespace attribute = %v, want example", got)
			}
		})
	}
}

func testService(t *testing.T) *Service {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	cfg := &config.Config{
		Debrids: []config.Debrid{
			{Provider: "realdebrid", Name: "rd-main"},
			{Provider: "torbox", Name: "tb"},
		},
	}
	cfg.Hearsay.AdviceMode = "active"
	cfg.Usenet.Providers = []config.UsenetProvider{{Host: "news.example", Backbone: "omicron"}}
	s, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("service unexpectedly disabled")
	}
	t.Cleanup(s.Close)
	return s
}

func TestDisabledIsInert(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid"}}}
	cfg.Hearsay.Disabled = true
	s, err := New(cfg, zerolog.Nop())
	if err != nil || s != nil {
		t.Fatalf("want nil service, got %v, %v", s, err)
	}
	s.ObserveTorrent("realdebrid", "abc", true)
	s.ReportAdd("realdebrid", "abc", true)
	s.ReportNZB("abc", true)
	if s.NZBClaimedIncomplete("abc") {
		t.Fatal("nil service should never claim incomplete")
	}
	if s.EvaluateAdd("realdebrid", "abc").Reject() {
		t.Fatal("nil service should never gate a submit")
	}
	s.Close()
}

func TestNetworkPublisherShadowDefaults(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid"}}}
	s, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	status := s.Status()
	if !status.Participate || !status.Publish {
		t.Fatalf("network defaults disabled: %+v", status)
	}
	if status.Protocol != hearsaylib.ProtocolVersion || status.AdviceMode != "shadow" {
		t.Fatalf("protocol and advice mode = %q, %q", status.Protocol, status.AdviceMode)
	}
}

func TestExplicitNetworkOptOut(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid"}}}
	cfg.Hearsay.Participate = new(false)
	s, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	status := s.Status()
	if status.Participate || status.Publish {
		t.Fatalf("network opt-out ignored: %+v", status)
	}
}

func TestInvalidConfigurationDisablesHearsay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Hearsay)
	}{
		{name: "mode", mutate: func(cfg *config.Hearsay) { cfg.AdviceMode = "automatic" }},
		{name: "support", mutate: func(cfg *config.Hearsay) { cfg.MinSupport = 1.1 }},
		{name: "storage", mutate: func(cfg *config.Hearsay) { cfg.MaxStorageBytes = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config.SetConfigPath(t.TempDir())
			cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid"}}}
			test.mutate(&cfg.Hearsay)
			if service, err := New(cfg, zerolog.Nop()); err == nil || service != nil {
				t.Fatalf("service, error = %v, %v", service, err)
			}
		})
	}
}

// TestNZBClaimedIncompleteExpires pins that a local miss gates the
// retry storm but not forever: a post marked missing while it was
// still propagating must get another stat once the window passes, or
// the gate prevents the check that would correct it.
func TestNZBClaimedIncompleteExpires(t *testing.T) {
	s := testService(t)
	const subject = "2c6b6858d61da9543d4231a71db4b1c9264b06852c6b6858d61da9543d4231a7"

	if s.NZBClaimedIncomplete(subject) {
		t.Fatal("unknown subject must not gate")
	}
	s.ReportNZB(subject, false)
	if !s.NZBClaimedIncomplete(subject) {
		t.Fatal("fresh local miss must gate")
	}
	s.ReportNZB(subject, true)
	if s.NZBClaimedIncomplete(subject) {
		t.Fatal("a complete import must clear the gate")
	}

	// The store keeps the newest observation, so age a subject by
	// recording it stale outright rather than overwriting a fresh one.
	const stale = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := s.engine.Observe("usenet.omicron.complete", stale, 0, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.NZBClaimedIncomplete(stale) {
		t.Fatal("stale local miss must not gate; one stat should re-verify")
	}
}

func TestActiveAdviceUsesLocalTruth(t *testing.T) {
	s := testService(t)
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	decision := s.EvaluateAdd("realdebrid", ih)
	if decision.Reject() {
		t.Fatal("unknown subject must not gate a submit")
	}
	s.DiscardAdd(decision)
	s.ObserveTorrent("realdebrid", ih, false)
	decision = s.EvaluateAdd("realdebrid", ih)
	if !decision.Reject() {
		t.Fatal("fresh local negative must gate")
	}
	s.DiscardAdd(decision)
	decision = s.EvaluateAdd("torbox", ih)
	if decision.Reject() {
		t.Fatal("another vendor's truth must not gate")
	}
	s.DiscardAdd(decision)
	s.ObserveTorrent("realdebrid", ih, true)
	decision = s.EvaluateAdd("realdebrid", ih)
	if decision.Reject() {
		t.Fatal("local positive must not gate")
	}
	s.DiscardAdd(decision)
}

func TestShadowAdviceMeasuresWithoutGating(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid"}}}
	s, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	s.ObserveTorrent("realdebrid", ih, false)
	decision := s.EvaluateAdd("realdebrid", ih)
	if decision.Reject() {
		t.Fatal("shadow advice must not gate")
	}
	s.RecordAdd(decision, false)
	metrics := s.Status().Advice["realdebrid"]
	if metrics.Evaluations != 1 || metrics.Negative != 1 || metrics.Outcomes != 1 || metrics.Correct != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestActiveAdviceRequiresEarnedEvidence(t *testing.T) {
	s := testService(t)
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	negativeTraining := []string{
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
	}
	ingestPeer(t, s, hsdebrid.NewUncached("realdebrid"), append([]string{ih}, negativeTraining...))
	decision := s.EvaluateAdd("realdebrid", ih)
	if decision.Reject() {
		t.Fatal("unverified evidence must not gate")
	}
	s.DiscardAdd(decision)
	for _, subject := range negativeTraining {
		s.ReportAdd("realdebrid", subject, false)
	}
	decision = s.EvaluateAdd("realdebrid", ih)
	if !decision.Reject() {
		t.Fatal("verified negative evidence must gate")
	}
	s.DiscardAdd(decision)

	positiveTraining := []string{
		"3333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444",
		"5555555555555555555555555555555555555555",
	}
	ingestPeer(t, s, hsdebrid.New("realdebrid"), append([]string{ih}, positiveTraining...))
	for _, subject := range positiveTraining {
		s.ReportAdd("realdebrid", subject, true)
	}
	decision = s.EvaluateAdd("realdebrid", ih)
	if decision.Reject() {
		t.Fatal("stronger positive evidence must override a denial")
	}
	s.DiscardAdd(decision)
}

func TestObserveAndReport(t *testing.T) {
	s := testService(t)
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	s.ObserveTorrent("realdebrid", ih, true)
	s.ObserveTorrent("unknown-vendor", ih, true)
	s.ReportAdd("realdebrid", ih, false)
	s.ReportNZB(NZBSubjectFromGroups(map[string]*parser.FileGroup{
		"a": {
			Type:  storage.NZBFileTypeMedia,
			Files: []manifest.File{{Segments: []manifest.Segment{{MessageID: "m@x"}}}},
		},
	}), true)

	a, err := s.engine.Query("debrid.realdebrid.cached", ih)
	if err != nil || !a.Local {
		t.Fatalf("truth not recorded: %+v, %v", a, err)
	}
	if a.Value != 0 {
		t.Fatal("report outcome should have overwritten the observation")
	}
}

// A follow list is an allowlist: feeds a previous unrestricted run
// discovered must stop answering queries once the operator narrows to
// an explicit set.
func TestFollowDropsUnlistedFeeds(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigPath(dir)
	cfg := &config.Config{Debrids: []config.Debrid{{Provider: "realdebrid", Name: "rd"}}}

	// First run: no follow list, so a discovered feed is retained.
	open, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	const ns = "debrid.realdebrid.cached"
	const infohash = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	ingestPeer(t, open, hsdebrid.New("realdebrid"), []string{infohash})
	if a, _ := open.engine.Query(ns, infohash); a.Sources != 1 {
		t.Fatalf("setup: sources = %d", a.Sources)
	}
	open.Close()

	// Second run: follow a different publisher.
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Hearsay.Follow = []string{"ed25519:" + hex.EncodeToString(other)}
	s, err := New(cfg, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if a, _ := s.engine.Query(ns, infohash); a.Sources != 0 {
		t.Fatalf("feed outside the follow list still answers: %+v", a)
	}
	if got := s.engine.Feeds(ns); len(got) != 0 {
		t.Fatalf("feeds = %v", got)
	}
}

func ingestPeer(t *testing.T, service *Service, domain hearsaylib.Domain, subjects []string) {
	t.Helper()
	peer, err := hearsaylib.New(t.TempDir(), domain)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	for _, subject := range subjects {
		if err := peer.Observe(domain.Namespace(), subject, 1, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	generation, err := peer.Publish(domain.Namespace())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := generation.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.engine.Ingest(raw); err != nil {
		t.Fatal(err)
	}
}
