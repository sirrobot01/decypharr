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
	_, ok := errors.AsType[*PanicError](err)
	return ok
}
