package customerror

import (
	"errors"
	"fmt"
)

type PanicError struct {
	e any
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic: %v", e.e)
}

func (e *PanicError) Unwrap() error {
	err, _ := e.e.(error)
	return err
}

func NewPanicError(e any) error {
	return &PanicError{e: e}
}

func IsPanicError(err error) bool {
	_, ok := errors.AsType[*PanicError](err)
	return ok
}
