package nntp

import "context"

// repairDefaultPercent is used when cfg.Repair.NNTPConnectionPercent is unset.
const repairDefaultPercent = 20

// RepairBank is a counting semaphore that caps how many NNTP connections
// batch availability checks can use concurrently across the whole client.
// One bank is constructed per Client at startup; each BatchStat worker holds
// one token for its lifetime.
type RepairBank struct {
	tokens chan struct{}
}

// newRepairBank returns a bank sized as `percent` of the client's total
// provider connections. Returns nil when no budget can be allocated.
func (c *Client) newRepairBank(percent int) *RepairBank {
	if c == nil {
		return nil
	}
	if percent <= 0 {
		percent = repairDefaultPercent
	}
	if percent > 100 {
		percent = 100
	}
	total := c.TotalConnections()
	if total <= 0 {
		return nil
	}
	capacity := (total*percent + 99) / 100
	if capacity < 1 {
		capacity = 1
	}
	if capacity > total {
		capacity = total
	}
	b := &RepairBank{tokens: make(chan struct{}, capacity)}
	for i := 0; i < capacity; i++ {
		b.tokens <- struct{}{}
	}
	return b
}

// TotalConnections is the sum of MaxConnections across configured providers.
func (c *Client) TotalConnections() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, p := range c.providers {
		if p.MaxConnections > 0 {
			total += p.MaxConnections
		}
	}
	return total
}

// Capacity returns the total number of tokens the bank can hand out.
func (b *RepairBank) Capacity() int {
	if b == nil {
		return 0
	}
	return cap(b.tokens)
}

// acquire takes one token from the bank, blocking until one is available or
// ctx is cancelled. The returned release func returns the token to the bank.
// A nil bank is a no-op so callers don't need to branch.
func (b *RepairBank) acquire(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.tokens:
		return func() { b.tokens <- struct{}{} }, nil
	}
}
