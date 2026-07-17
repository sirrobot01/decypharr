package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// newFakeFFProbeBinary writes a stand-in ffprobe executable that ignores its
// arguments and always prints the given JSON to stdout - enough to drive
// check()'s first (format+streams) probe without a real ffprobe binary or a
// real media file. tailIntact's own ffprobe call is covered separately by
// stubbing tailIntactFn, so the fake binary only ever needs to answer that
// first call.
func newFakeFFProbeBinary(t *testing.T, stdoutJSON string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\ncat <<'EOF'\n" + stdoutJSON + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}
	return path
}

func newTestFFProbeChecker(t *testing.T, probedSeconds int, tailIntactFn func(ctx context.Context, entryFolder, fileName string, probed time.Duration) bool) *ffprobeChecker {
	t.Helper()
	// ffprobe's format.duration is a plain float string of seconds.
	stdout := fmt.Sprintf(`{"format":{"duration":"%d.000000"},"streams":[{"codec_type":"video"},{"codec_type":"audio"}]}`, probedSeconds)
	bin := newFakeFFProbeBinary(t, stdout)
	return &ffprobeChecker{
		binPath:      bin,
		timeout:      5 * time.Second,
		baseURL:      "http://127.0.0.1:1/",
		logger:       zerolog.Nop(),
		tailIntactFn: tailIntactFn,
	}
}

func TestCheck_TooShort_TailIntact_NotBroken(t *testing.T) {
	f := newTestFFProbeChecker(t, 900, func(ctx context.Context, entryFolder, fileName string, probed time.Duration) bool {
		return true
	})

	ok, reason := f.check(context.Background(), "Some.Show.S01E01", "episode.mkv", expectedRuntime{
		Seconds:               3600,
		EpisodeCountConfirmed: true,
	})

	if !ok {
		t.Fatalf("expected ok=true (tail intact => metadata mismatch, not broken), got ok=false reason=%q", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

func TestCheck_TooShort_TailBroken_MarkedBroken(t *testing.T) {
	f := newTestFFProbeChecker(t, 900, func(ctx context.Context, entryFolder, fileName string, probed time.Duration) bool {
		return false
	})

	ok, reason := f.check(context.Background(), "Some.Show.S01E01", "episode.mkv", expectedRuntime{
		Seconds:               3600,
		EpisodeCountConfirmed: true,
	})

	if ok {
		t.Fatalf("expected ok=false (tail unreadable => broken), got ok=true")
	}
	if !strings.Contains(reason, "tail_unreadable") {
		t.Errorf("reason %q missing tail_unreadable suffix", reason)
	}
	if !strings.Contains(reason, ffprobeReasonRuntimeMismatch) {
		t.Errorf("reason %q missing %s", reason, ffprobeReasonRuntimeMismatch)
	}
}

func TestCheck_NormalRuntime_DoesNotCallTailIntact(t *testing.T) {
	called := false
	f := newTestFFProbeChecker(t, 3600, func(ctx context.Context, entryFolder, fileName string, probed time.Duration) bool {
		called = true
		return true
	})

	ok, reason := f.check(context.Background(), "Some.Show.S01E01", "episode.mkv", expectedRuntime{
		Seconds:               3600,
		EpisodeCountConfirmed: true,
	})

	if !ok || reason != "" {
		t.Fatalf("expected ok=true, empty reason for a matching runtime; got ok=%v reason=%q", ok, reason)
	}
	if called {
		t.Error("tailIntactFn should not be called when the ratio isn't too-short")
	}
}

func newTailIntactChecker(t *testing.T, script string) *ffprobeChecker {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}
	return &ffprobeChecker{
		binPath: path,
		timeout: 5 * time.Second,
		baseURL: "http://127.0.0.1:1/",
		logger:  zerolog.Nop(),
	}
}

func TestTailIntact_PacketNearProbedEnd_ReturnsTrue(t *testing.T) {
	f := newTailIntactChecker(t, "#!/bin/sh\ncat <<'EOF'\n"+`{"packets":[{"pts_time":"895.500000"}]}`+"\nEOF\n")

	if !f.tailIntact(context.Background(), "entry", "file.mkv", 900*time.Second) {
		t.Error("expected tailIntact=true for a packet within 45s of the probed duration")
	}
}

func TestTailIntact_NoPacketsNearEnd_ReturnsFalse(t *testing.T) {
	f := newTailIntactChecker(t, "#!/bin/sh\ncat <<'EOF'\n"+`{"packets":[{"pts_time":"10.000000"}]}`+"\nEOF\n")

	if f.tailIntact(context.Background(), "entry", "file.mkv", 900*time.Second) {
		t.Error("expected tailIntact=false when no packet is within 45s of the probed duration")
	}
}

func TestTailIntact_ExecError_ReturnsFalse(t *testing.T) {
	f := newTailIntactChecker(t, "#!/bin/sh\nexit 1\n")

	if f.tailIntact(context.Background(), "entry", "file.mkv", 900*time.Second) {
		t.Error("expected tailIntact=false on a non-zero ffprobe exit")
	}
}

func TestTailIntact_UnparsableOutput_ReturnsFalse(t *testing.T) {
	f := newTailIntactChecker(t, "#!/bin/sh\necho 'not json'\n")

	if f.tailIntact(context.Background(), "entry", "file.mkv", 900*time.Second) {
		t.Error("expected tailIntact=false on unparsable ffprobe output")
	}
}
