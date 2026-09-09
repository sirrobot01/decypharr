package customerror

import (
	"errors"
	"fmt"
	"testing"
)

func TestPanicErrorPreservesPayload(t *testing.T) {
	sentinel := errors.New("failed")
	for _, payload := range []any{"text", 42, nil, sentinel} {
		err := NewPanicError(payload)
		if got, want := err.Error(), fmt.Sprintf("panic: %v", payload); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		cause, _ := payload.(error)
		if got := errors.Unwrap(err); got != cause {
			t.Errorf("Unwrap() = %v, want %v", got, cause)
		}
		if !IsPanicError(fmt.Errorf("worker: %w", err)) {
			t.Error("wrapped panic was not recognized")
		}
	}
	if !errors.Is(NewPanicError(sentinel), sentinel) {
		t.Error("error identity was lost")
	}
}
