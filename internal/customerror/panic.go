package customerror

import (
	"errors"
	"fmt"
)

type PanicError struct {
	e any
}

func (e *PanicError) Error() string {
	return "panic: " + e.e.(string)
}

func (e *PanicError) Unwrap() error {
	return fmt.Errorf("panic: %v", e.e)
}

func NewPanicError(e any) error {
	return &PanicError{e: e}
}

func IsPanicError(err error) bool {
	var panicError *PanicError
	ok := errors.As(err, &panicError)
	return ok
}
