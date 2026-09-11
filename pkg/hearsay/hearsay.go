// Package hearsay connects Decypharr's real outcomes to an embedded
// Hearsay engine. Network sharing is enabled by default; active decisions
// remain opt-in.
package hearsay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	hearsaylib "github.com/sirrobot01/hearsay"
	hsdebrid "github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/transport"
	hsusenet "github.com/sirrobot01/hearsay/usenet"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

type Service struct {
	engine      *hearsaylib.Hearsay
	log         zerolog.Logger
	debrids     map[string]string
	uncached    map[string]string
	usenets     map[string]string
	advisors    map[string]*hsdebrid.Advisor
	adviceMode  hsdebrid.AdviceMode
	policy      hsdebrid.AdvicePolicy
	participate bool
	publish     bool
	interval    time.Duration
	port        int
	gossip      int
	maxStorage  int64
	maxFeeds    int
	follow      []string

	mu        sync.Mutex
	node      *transport.Node
	closeOnce sync.Once
}

// New builds the service from configuration. It returns (nil, nil)
// when hearsay is disabled or no observable domain is configured; a
// nil *Service is safe to use everywhere.
func New(cfg *config.Config, log zerolog.Logger) (*Service, error) {
	if cfg.Hearsay.Disabled {
		return nil, nil
	}
	if cfg.Hearsay.MaxStorageBytes < 0 || cfg.Hearsay.MaxFeedsPerNamespace < 0 {
		return nil, fmt.Errorf("hearsay: transport limits must not be negative")
	}
	mode, err := parseAdviceMode(cfg.Hearsay.AdviceMode)
	if err != nil {
		return nil, err
	}
	policy, err := advicePolicy(cfg.Hearsay)
	if err != nil {
		return nil, err
	}
	s := &Service{
		log:         log.With().Str("component", "hearsay").Logger(),
		debrids:     map[string]string{},
		uncached:    map[string]string{},
		usenets:     map[string]string{},
		advisors:    map[string]*hsdebrid.Advisor{},
		adviceMode:  mode,
		policy:      policy,
		participate: cfg.Hearsay.Participates(),
		publish:     cfg.Hearsay.Publishes(),
		port:        cfg.Hearsay.Port,
		gossip:      cfg.Hearsay.GossipPort,
		maxStorage:  cfg.Hearsay.MaxStorageBytes,
		maxFeeds:    cfg.Hearsay.MaxFeedsPerNamespace,
	}
	var domains []hearsaylib.Domain
	for _, d := range cfg.Debrids {
		vendor := strings.ToLower(strings.TrimSpace(d.Provider))
		if vendor == "" {
			continue
		}
		if _, dup := s.debrids[vendor]; dup {
			continue
		}
		dom := hsdebrid.New(vendor)
		undom := hsdebrid.NewUncached(vendor)
		s.debrids[vendor] = dom.Namespace()
		s.uncached[vendor] = undom.Namespace()
		domains = append(domains, dom, undom)
	}
	for _, p := range cfg.Usenet.Providers {
		// The explicit failover backbone wins; otherwise infer it from
		// the host so operators do not need to know their backbone.
		backbone := strings.ToLower(strings.TrimSpace(p.Backbone))
		if backbone == "" {
			backbone = hsusenet.InferBackbone(p.Host)
		}
		if backbone == "" {
			continue
		}
		if _, dup := s.usenets[backbone]; dup {
			continue
		}
		dom := hsusenet.New(backbone)
		s.usenets[backbone] = dom.Namespace()
		domains = append(domains, dom)
	}
	if len(domains) == 0 {
		return nil, nil
	}
	if cfg.Hearsay.Interval != "" {
		interval, err := time.ParseDuration(cfg.Hearsay.Interval)
		if err != nil {
			return nil, fmt.Errorf("hearsay interval: %w", err)
		}
		s.interval = interval
	}
	for _, f := range cfg.Hearsay.Follow {
		key := strings.ToLower(strings.TrimSpace(f))
		s.follow = append(s.follow, strings.TrimPrefix(key, "ed25519:"))
	}
	dir := filepath.Join(config.GetMainPath(), "hearsay")
	engine, err := hearsaylib.New(dir, domains...)
	if err != nil {
		return nil, err
	}
	s.engine = engine
	for vendor := range maps.Keys(s.debrids) {
		advisor, err := hsdebrid.NewAdvisorWithPolicy(
			engine,
			vendor,
			mode,
			policy,
			filepath.Join(dir, "advice", vendor+".json"),
		)
		if err != nil {
			engine.Close()
			return nil, err
		}
		s.advisors[vendor] = advisor
	}
	// A non-empty follow list is an allowlist, not a set of extra seeds.
	// Retained generations answer queries whether or not the network is
	// running, so drop what an earlier, unrestricted run discovered.
	// Failing here disables participation, which is the safe outcome:
	// better no hints than hints from a publisher the operator dropped.
	if len(s.follow) > 0 {
		self := hex.EncodeToString(engine.Identity())
		for _, ns := range s.namespaces() {
			for _, feed := range engine.Feeds(ns) {
				if feed == self || slices.Contains(s.follow, feed) {
					continue
				}
				if err := engine.Forget(ns, feed); err != nil {
					engine.Close()
					return nil, fmt.Errorf("hearsay: dropping feed outside the follow list: %w", err)
				}
				s.log.Debug().Str("ns", ns).Str("feed", feed[:8]).Msg("dropped feed outside the follow list")
			}
		}
	}
	return s, nil
}

func parseAdviceMode(value string) (hsdebrid.AdviceMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "shadow":
		return hsdebrid.Shadow, nil
	case "active":
		return hsdebrid.Active, nil
	default:
		return 0, fmt.Errorf("hearsay: advice mode must be shadow or active")
	}
}

func advicePolicy(cfg config.Hearsay) (hsdebrid.AdvicePolicy, error) {
	policy := hsdebrid.DefaultAdvicePolicy()
	if cfg.MinSupport != 0 {
		policy.MinSupport = cfg.MinSupport
	}
	if cfg.MinEvidence != 0 {
		policy.MinEvidence = cfg.MinEvidence
	}
	if cfg.MinSources != 0 {
		policy.MinSources = cfg.MinSources
	}
	if policy.MinSupport < 0 || policy.MinSupport > 1 || policy.MinEvidence < 0 || policy.MinSources < 1 {
		return hsdebrid.AdvicePolicy{}, fmt.Errorf("hearsay: invalid advice thresholds")
	}
	return policy, nil
}

// Start joins the network: seed and publish own generations, discover
// and fetch others'. Runs until ctx is done.
func (s *Service) Start(ctx context.Context) error {
	if s == nil || !s.participate {
		return nil
	}
	node, err := transport.ListenWithConfig(transport.NodeConfig{
		Dir:                  filepath.Join(config.GetMainPath(), "hearsay", "swarm"),
		Port:                 s.port,
		GossipPort:           s.gossip,
		MaxStorage:           s.maxStorage,
		MaxFeedsPerNamespace: s.maxFeeds,
	})
	if err != nil {
		return fmt.Errorf("hearsay transport: %w", err)
	}
	s.mu.Lock()
	s.node = node
	s.mu.Unlock()
	// With a follow list the node accepts nothing else: no DHT
	// announce, and no keys from the gossip handshake. Own key included,
	// or the node cannot advertise its own feed.
	if len(s.follow) > 0 {
		node.Allow(append(slices.Clone(s.follow), hex.EncodeToString(s.engine.Identity()))...)
	}
	for _, ns := range s.namespaces() {
		node.Track(ns)
		for _, key := range s.follow {
			node.PinFeed(ns, key)
		}
	}
	syncer := &transport.Syncer{
		Engine:   s.engine,
		Node:     node,
		Publish:  s.publish,
		Interval: s.interval,
		Log:      slog.New(zerologHandler{log: s.log}),
	}
	go syncer.Run(ctx)
	context.AfterFunc(ctx, s.Close)
	s.log.Info().
		Str("identity", "ed25519:"+hex.EncodeToString(s.engine.Identity())).
		Int("namespaces", len(s.namespaces())).
		Bool("publish", s.publish).
		Msg("Participating in hearsay network")
	return nil
}

func (s *Service) namespaces() []string {
	out := make([]string, 0, len(s.debrids)+len(s.uncached)+len(s.usenets))
	out = append(out, slices.Collect(maps.Values(s.debrids))...)
	out = append(out, slices.Collect(maps.Values(s.uncached))...)
	out = append(out, slices.Collect(maps.Values(s.usenets))...)
	slices.Sort(out)
	return out
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		node := s.node
		s.mu.Unlock()
		if node != nil {
			node.Close()
		}
		if err := s.engine.Close(); err != nil {
			s.log.Debug().Err(err).Msg("hearsay close")
		}
	})
}

// Status reports participation state for the API and UI.
type Status struct {
	Enabled     bool                               `json:"enabled"`
	Protocol    string                             `json:"protocol,omitempty"`
	Participate bool                               `json:"participate"`
	Publish     bool                               `json:"publish"`
	AdviceMode  string                             `json:"advice_mode,omitempty"`
	Identity    string                             `json:"identity,omitempty"`
	Feeds       map[string]int                     `json:"feeds,omitempty"`
	Metrics     hearsaylib.EngineMetrics           `json:"metrics"`
	Advice      map[string]hsdebrid.AdvisorMetrics `json:"advice,omitempty"`
	Transport   *transport.Stats                   `json:"transport,omitempty"`
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	st := Status{
		Enabled:     true,
		Protocol:    hearsaylib.ProtocolVersion,
		Participate: s.participate,
		Publish:     s.publish,
		AdviceMode:  adviceModeName(s.adviceMode),
		Identity:    "ed25519:" + hex.EncodeToString(s.engine.Identity()),
		Feeds:       map[string]int{},
		Metrics:     s.engine.Metrics(),
		Advice:      map[string]hsdebrid.AdvisorMetrics{},
	}
	for _, ns := range s.namespaces() {
		st.Feeds[ns] = len(s.engine.Feeds(ns))
	}
	for vendor, advisor := range s.advisors {
		st.Advice[vendor] = advisor.Snapshot()
	}
	s.mu.Lock()
	node := s.node
	s.mu.Unlock()
	if node != nil {
		stats := node.Stats()
		st.Transport = &stats
	}
	return st
}

func adviceModeName(mode hsdebrid.AdviceMode) string {
	if mode == hsdebrid.Active {
		return "active"
	}
	return "shadow"
}

// ObserveTorrent records ground truth learned from a repair probe:
// the file is (or is not) present on the provider. Both paired
// namespaces record it — an absence is a positive claim in the
// uncached namespace, the only form a bloom can publish.
func (s *Service) ObserveTorrent(vendor, infohash string, present bool) {
	if s == nil || infohash == "" {
		return
	}
	vendor = strings.ToLower(vendor)
	ns, ok := s.debrids[vendor]
	if !ok {
		return
	}
	value := 0.0
	if present {
		value = 1
	}
	now := time.Now()
	if err := s.engine.Observe(ns, infohash, value, now); err != nil {
		s.log.Debug().Err(err).Str("ns", ns).Msg("observe failed")
	}
	if err := s.engine.Observe(s.uncached[vendor], infohash, 1-value, now); err != nil {
		s.log.Debug().Err(err).Str("ns", s.uncached[vendor]).Msg("observe failed")
	}
}

// ReportAdd feeds back the outcome of an actual add: instantly served
// from cache or not. Both paired namespaces score their sources from
// the one outcome — an instant add confirms cached claims and refutes
// uncached ones, and each domain's Verify knows its own polarity.
func (s *Service) ReportAdd(vendor, infohash string, instant bool) {
	if s == nil || infohash == "" {
		return
	}
	vendor = strings.ToLower(vendor)
	if _, ok := s.debrids[vendor]; !ok {
		return
	}
	for _, ns := range []string{s.debrids[vendor], s.uncached[vendor]} {
		if err := s.engine.Report(ns, infohash, instant); err != nil {
			s.log.Debug().Err(err).Str("ns", ns).Msg("report failed")
		}
	}
}

type AddDecision struct {
	advisor *hsdebrid.Advisor
	id      string
	vendor  string
	subject string
	reject  bool
}

func (d AddDecision) Reject() bool {
	return d.reject
}

func (s *Service) EvaluateAdd(vendor, infohash string) AddDecision {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	result := AddDecision{vendor: vendor, subject: infohash}
	if s == nil || infohash == "" {
		return result
	}
	advisor := s.advisors[vendor]
	if advisor == nil {
		return result
	}
	decision, err := advisor.Evaluate(infohash)
	if err != nil {
		s.log.Debug().Err(err).Str("provider", vendor).Msg("advice evaluation failed")
		return result
	}
	result.advisor = advisor
	result.id = decision.ID
	result.reject = decision.UseHint && decision.State == hearsaylib.EvidenceNegative
	return result
}

func (s *Service) RecordAdd(decision AddDecision, instant bool) {
	if s == nil || decision.subject == "" {
		return
	}
	if decision.advisor == nil || decision.id == "" {
		s.ReportAdd(decision.vendor, decision.subject, instant)
		return
	}
	if err := decision.advisor.RecordOutcome(decision.id, instant); err != nil {
		s.log.Debug().Err(err).Str("provider", decision.vendor).Msg("advice outcome failed")
	}
}

func (s *Service) DiscardAdd(decision AddDecision) {
	if decision.advisor != nil && decision.id != "" {
		decision.advisor.Discard(decision.id)
	}
}

// ReportNZB feeds back an import-time completeness check: it records
// local truth and scores every followed source that claimed on the
// subject. A definitive miss is attributed to every configured
// backbone — the availability check established the segments are
// missing on all providers. A success is attributed only when a
// single backbone is configured, because the check does not reveal
// which provider answered.
func (s *Service) ReportNZB(subject string, complete bool) {
	if s == nil || subject == "" {
		return
	}
	if complete && len(s.usenets) != 1 {
		return
	}
	for ns := range maps.Values(s.usenets) {
		if err := s.engine.Report(ns, subject, complete); err != nil {
			s.log.Debug().Err(err).Str("ns", ns).Msg("report failed")
		}
	}
}

// NZBClaimedIncomplete reports whether local truth or a strong network
// consensus says the post is incomplete on every configured backbone.
// The thresholds are conservative on purpose: a wrong rejection loses
// a repairable release and, because the availability check never runs,
// the mistake is never observed or corrected. Own truth is trusted
// outright; the network needs corroboration, a low completeness mean,
// and fresh confident sources.
func (s *Service) NZBClaimedIncomplete(subject string) bool {
	if s == nil || s.adviceMode != hsdebrid.Active || subject == "" || len(s.usenets) == 0 {
		return false
	}
	for ns := range maps.Values(s.usenets) {
		a, err := s.engine.Query(ns, subject)
		if err != nil || !a.Known {
			return false
		}
		if a.Local {
			// Trust own negatives for a day: long enough to absorb
			// the *arr's retry storm, short enough that a post marked
			// missing while still propagating gets re-checked. One
			// stat re-verifies after expiry, and a genuinely dead
			// post re-records instantly. ReportNZB writes a miss to
			// every backbone, so one namespace answers for all.
			return a.Value < 0.5 && a.Age <= 24*time.Hour
		}
		if a.Value > 0.2 || a.Age > 24*time.Hour || a.Support < s.policy.MinSupport ||
			a.EvidenceWeight < s.policy.MinEvidence || a.Sources < max(2, s.policy.MinSources) {
			return false
		}
	}
	return true
}

// zerologHandler maps slog records from the hearsay syncer onto
// decypharr's zerolog logger. High-volume progress remains available at
// debug level, while transient peer availability failures use trace.
type zerologHandler struct {
	log   zerolog.Logger
	attrs []slog.Attr
}

const slogLevelTrace = slog.LevelDebug - 4

func (h zerologHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h zerologHandler) Handle(_ context.Context, r slog.Record) error {
	level := hearsayLogLevel(r.Level, r.Message)
	var e *zerolog.Event
	switch {
	case level >= slog.LevelError:
		e = h.log.Error()
	case level >= slog.LevelWarn:
		e = h.log.Warn()
	case level >= slog.LevelInfo:
		e = h.log.Info()
	case level >= slog.LevelDebug:
		e = h.log.Debug()
	default:
		e = h.log.Trace()
	}
	// zerolog serializes opaque error structs to "{}"; keep the message.
	add := func(key string, value any) {
		if err, ok := value.(error); ok {
			e = e.Str(key, err.Error())
			return
		}
		e = e.Any(key, value)
	}
	for _, a := range h.attrs {
		add(a.Key, a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		add(a.Key, a.Value.Any())
		return true
	})
	e.Msg(r.Message)
	return nil
}

func hearsayLogLevel(level slog.Level, message string) slog.Level {
	switch {
	case level == slog.LevelInfo && (message == "published generation" || message == "ingested generation"):
		return slog.LevelDebug
	case level == slog.LevelWarn && message == "fetch failed":
		return slogLevelTrace
	default:
		return level
	}
}

func (h zerologHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clip(h.attrs), attrs...)
	return h
}

func (h zerologHandler) WithGroup(string) slog.Handler { return h }

// NZBSubjectFromGroups derives the subject from the parsed file groups,
// which is the only form available before Process runs. Process is what
// fills in NZB.Files, so deriving the subject from the NZB any earlier
// hashes an empty list and yields "".
func NZBSubjectFromGroups(groups map[string]*parser.FileGroup) string {
	var ids []string
	for _, g := range groups {
		if g == nil {
			continue
		}
		for i := range g.Files {
			for _, seg := range g.Files[i].Segments {
				if seg.MessageID != "" {
					ids = append(ids, seg.MessageID)
				}
			}
		}
	}
	return subjectFromIDs(ids)
}

func subjectFromIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
