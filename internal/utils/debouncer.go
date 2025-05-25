package utils

import (
	"sync"
	"time"
)

type Debouncer[T any] struct {
	mu       sync.Mutex
	timer    *time.Timer
	interval time.Duration
	caller   func(arg T)
}

func NewDebouncer[T any](interval time.Duration, caller func(arg T)) *Debouncer[T] {
	return &Debouncer[T]{
		interval: interval,
		caller:   caller,
	}
}

func (d *Debouncer[T]) Call(arg T) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
	}

	d.timer = time.AfterFunc(d.interval, func() {
		d.caller(arg)
	})
}

func (d *Debouncer[T]) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
