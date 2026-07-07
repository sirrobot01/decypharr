package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	json "github.com/bytedance/sonic"
	"github.com/rs/zerolog"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Reasons are prefixed "ffprobe_" so they're greppable in health records
// alongside the STAT-probe reasons (usenet_segment_missing, etc).
const (
	ffprobeReasonUnreadable      = "ffprobe_unreadable"
	ffprobeReasonNoStreams       = "ffprobe_no_streams"
	ffprobeReasonNoVideoStream   = "ffprobe_no_video_stream"
	ffprobeReasonNoDuration      = "ffprobe_no_duration"
	ffprobeReasonRuntimeMismatch = "ffprobe_runtime_mismatch"
	ffprobeReasonAbsurdDuration  = "ffprobe_absurd_duration"
)

const (
	ffprobeDefaultTimeout = 90 * time.Second
	ffprobeRetryDelay     = 2 * time.Second

	// TOO LONG: broken when the file runs at least this many times its
	// expected runtime AND is at least ffprobeTooLongMinOverage over. The
	// unconfirmed ratio is wider so an unrecognized double-episode file that
	// happens to land at exactly 2x isn't deleted for being correct; real
	// extended cuts run ~1.3-1.4x, well under either threshold.
	ffprobeTooLongRatioConfirmed   = 2.0
	ffprobeTooLongRatioUnconfirmed = 2.5
	ffprobeTooLongMinOverage       = 60 * time.Minute

	// TOO SHORT: broken when under half the expected runtime and at least 15
	// minutes short (truncated assembly).
	ffprobeTooShortRatio    = 0.5
	ffprobeTooShortMinUnder = 15 * time.Minute

	// Ceiling-only thresholds used when no expected runtime is available.
	ffprobeCeilingSonarr = 6 * time.Hour
	ffprobeCeilingOther  = 12 * time.Hour
)

// expectedRuntime is what the Arr believes a file's playback duration should
// be. Seconds == 0 means unknown - the checker falls back to a ceiling-only
// sanity check rather than a ratio comparison.
type expectedRuntime struct {
	Seconds               int
	EpisodeCountConfirmed bool
	ArrKind               storage.ArrKind
}

// ffprobeChecker validates one file's assembled stream by running ffprobe
// against decypharr's own local WebDAV endpoint. WebDAV supports HTTP Range,
// giving ffprobe the seekable access it needs (MP4s with a tail moov atom
// require a seek to EOF; a pipe would false-fail them), and since the read is
// served in-process it never touches the DFS FUSE downloaders - no risk of
// tripping the DFS circuit breaker or racing playback-escalation repair.
type ffprobeChecker struct {
	binPath string
	timeout time.Duration
	baseURL string // e.g. "http://127.0.0.1:8282/webdav/"

	// authToken is the manager's ephemeral, in-memory, per-process bearer
	// token (see webdav.Handler.isInternalBearer), sent via ffprobe's
	// -headers flag. It replaces the user's WebDAV password, which is only
	// ever stored as a bcrypt hash and so cannot be recovered and handed to
	// an external process. "" when WebDAV auth is off.
	//
	// Security note: this token appears in the ffprobe process's argument
	// list, visible to anything that can read /proc or run `ps` on this
	// host for the life of that (sub-second to low-second) process. That's
	// an acceptable trade-off on a typical single-user box, and the
	// ephemeral, restart-scoped nature of the token bounds the blast radius
	// of a leak to "until the next restart" rather than forever.
	authToken string

	logger zerolog.Logger
}

// newFFProbeChecker builds a checker for one sweep run, or returns nil (with
// exactly one WARN) when ffprobe_check is enabled but can't actually be used:
// binary missing, or WebDAV disabled. Callers must treat nil as "proceed
// STAT-only" rather than failing the sweep.
func newFFProbeChecker(cfg *config.Config, m *Manager, log zerolog.Logger) *ffprobeChecker {
	if !cfg.Repair.FFProbeCheck {
		return nil
	}

	binPath := strings.TrimSpace(cfg.Repair.FFProbePath)
	if binPath == "" {
		binPath = "ffprobe"
	}
	resolved, err := exec.LookPath(binPath)
	if err != nil {
		log.Warn().Err(err).Str("path", binPath).Msg("Repair: ffprobe_check is enabled but the ffprobe binary was not found on PATH; sweep will use STAT-only checks")
		return nil
	}
	if cfg.DisableWebDav {
		log.Warn().Msg("Repair: ffprobe_check is enabled but WebDAV is disabled (disable_webdav); sweep will use STAT-only checks")
		return nil
	}

	timeout := ffprobeDefaultTimeout
	if raw := strings.TrimSpace(cfg.Repair.FFProbeTimeout); raw != "" {
		if d, err := utils.ParseDuration(raw); err == nil && d > 0 {
			timeout = d
		} else {
			log.Warn().Str("value", raw).Msg("Repair: invalid repair.ffprobe_timeout; using default of 90s")
		}
	}

	var authToken string
	if cfg.UseAuth && cfg.EnableWebdavAuth {
		authToken = m.InternalToken()
	}

	return &ffprobeChecker{
		binPath:   resolved,
		timeout:   timeout,
		baseURL:   fmt.Sprintf("http://127.0.0.1:%s%swebdav/", cfg.Port, cfg.URLBase),
		authToken: authToken,
		logger:    log,
	}
}

type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

// check runs ffprobe once against entryFolder/fileName and returns whether
// the file looks healthy. A context timeout or cancellation is treated as
// inconclusive (ok=true) rather than broken - a slow cold read over Usenet
// must never cause an auto-delete.
func (f *ffprobeChecker) check(ctx context.Context, entryFolder, fileName string, expected expectedRuntime) (ok bool, reason string) {
	target := f.baseURL + EntryAllFolder + "/" + url.PathEscape(entryFolder) + "/" + url.PathEscape(fileName)

	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams"}
	if f.authToken != "" {
		args = append(args, "-headers", "Authorization: Bearer "+f.authToken+"\r\n")
	}
	args = append(args, target)

	cctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, f.binPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if cctx.Err() != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			f.logger.Debug().Str("entry", entryFolder).Str("file", fileName).Msg("Repair: ffprobe timed out; treating as inconclusive")
		}
		return true, ""
	}
	if runErr != nil {
		return false, ffprobeReasonUnreadable + ": " + firstLine(stderr.String())
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &probe); err != nil {
		return false, ffprobeReasonUnreadable + ": " + firstLine(err.Error())
	}

	hasVideo, hasAudio := false, false
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			hasVideo = true
		case "audio":
			hasAudio = true
		}
	}
	if !hasVideo && !hasAudio {
		return false, ffprobeReasonNoStreams
	}
	if !hasVideo {
		return false, ffprobeReasonNoVideoStream
	}

	durationSec, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || durationSec <= 0 {
		return false, ffprobeReasonNoDuration
	}
	duration := time.Duration(durationSec * float64(time.Second))

	if expected.Seconds <= 0 {
		ceiling := ffprobeCeilingOther
		if expected.ArrKind == storage.ArrKindSonarr {
			ceiling = ffprobeCeilingSonarr
		}
		if duration > ceiling {
			return false, ffprobeReasonAbsurdDuration
		}
		return true, ""
	}

	expectedDur := time.Duration(expected.Seconds) * time.Second
	ratio := duration.Seconds() / expectedDur.Seconds()

	tooLongRatio := ffprobeTooLongRatioConfirmed
	if !expected.EpisodeCountConfirmed {
		tooLongRatio = ffprobeTooLongRatioUnconfirmed
	}
	if ratio >= tooLongRatio && duration-expectedDur >= ffprobeTooLongMinOverage {
		return false, fmt.Sprintf("%s: probe=%dm expected=%dm (%.1fx)", ffprobeReasonRuntimeMismatch, int(duration.Minutes()), int(expectedDur.Minutes()), ratio)
	}
	if ratio <= ffprobeTooShortRatio && expectedDur-duration >= ffprobeTooShortMinUnder {
		return false, fmt.Sprintf("%s: probe=%dm expected=%dm (%.1fx)", ffprobeReasonRuntimeMismatch, int(duration.Minutes()), int(expectedDur.Minutes()), ratio)
	}

	// Grey zone: meaningfully different from expected but inside the safety
	// bands (extended cuts, specials, padded finales). Never auto-delete on
	// ambiguity - log it and let the file stand.
	if ratio < 0.9 || ratio > 1.1 {
		f.logger.Info().Str("entry", entryFolder).Str("file", fileName).Float64("ratio", ratio).Msg("Repair: ffprobe duration differs from expected but within tolerance; not marking broken")
	}
	return true, ""
}

// checkConfirmed retries once before declaring a file broken: a transient
// cold-seek can fail one read and pass the next, and since a broken verdict
// can lead to an automatic delete + re-search, a single bad read is never
// enough. Only a second consecutive broken verdict is returned as broken.
func (f *ffprobeChecker) checkConfirmed(ctx context.Context, entryFolder, fileName string, expected expectedRuntime) (ok bool, reason string) {
	ok, reason = f.check(ctx, entryFolder, fileName, expected)
	if ok {
		return true, ""
	}
	f.logger.Debug().Str("entry", entryFolder).Str("file", fileName).Str("reason", reason).Msg("Repair: ffprobe check failed; retrying once before declaring broken")

	select {
	case <-ctx.Done():
		return true, ""
	case <-time.After(ffprobeRetryDelay):
	}

	ok, reason = f.check(ctx, entryFolder, fileName, expected)
	f.logger.Debug().Str("entry", entryFolder).Str("file", fileName).Bool("ok", ok).Str("reason", reason).Msg("Repair: ffprobe retry result")
	if ok {
		return true, ""
	}
	return false, reason
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ffprobeCheckerCtxKey carries an optional *ffprobeChecker down through the
// probe call chain (probeAndHealCandidates -> probeEntry -> probeFiles ->
// probeFile), which is shared by the timed sweep, the on-demand sweep, and
// ad-hoc series/movie rechecks alike - a context value avoids threading a new
// parameter through every layer for what is, for two of those three
// call-sites, almost always nil.
type ffprobeCheckerCtxKey struct{}

func contextWithFFProbeChecker(ctx context.Context, checker *ffprobeChecker) context.Context {
	if checker == nil {
		return ctx
	}
	return context.WithValue(ctx, ffprobeCheckerCtxKey{}, checker)
}

func ffprobeCheckerFromContext(ctx context.Context) *ffprobeChecker {
	checker, _ := ctx.Value(ffprobeCheckerCtxKey{}).(*ffprobeChecker)
	return checker
}

// attachFFProbeChecker builds an ffprobe checker for this run when enabled
// and stores it on ctx for probeFile to pick up. newFFProbeChecker itself
// logs the one WARN when the flag is on but unusable (missing binary,
// WebDAV disabled); either way the sweep proceeds STAT-only.
func (r *Repair) attachFFProbeChecker(ctx context.Context, log zerolog.Logger) context.Context {
	cfg := config.Get()
	checker := newFFProbeChecker(cfg, r.manager, log)
	return contextWithFFProbeChecker(ctx, checker)
}

// expectedRuntimeFor resolves the expected playback duration for one file in
// a candidate entry from its Arr content mapping, when available.
func expectedRuntimeFor(c *candidate, name string) expectedRuntime {
	cf, ok := c.contentMap[name]
	if !ok {
		return expectedRuntime{ArrKind: c.arrKind}
	}
	return expectedRuntime{
		Seconds:               cf.RuntimeSec,
		EpisodeCountConfirmed: cf.EpisodeCountConfirmed,
		ArrKind:               c.arrKind,
	}
}
