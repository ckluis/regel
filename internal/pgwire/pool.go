package pgwire

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const dialCancelTimeout = 5 * time.Second

// Listen issues LISTEN on a channel; async notifications arrive on
// Notifications(). The channel name is quoted defensively.
func (c *Conn) Listen(ctx context.Context, channel string) error {
	_, err := c.ExecSimple(ctx, fmt.Sprintf("LISTEN %s", quoteIdent(channel)))
	return err
}

// Unlisten stops listening on a channel.
func (c *Conn) Unlisten(ctx context.Context, channel string) error {
	_, err := c.ExecSimple(ctx, fmt.Sprintf("UNLISTEN %s", quoteIdent(channel)))
	return err
}

func quoteIdent(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}

// Pool is a small bounded connection pool that enforces the destroy-on-desync
// discipline: a connection is returned to the idle set only if it is alive AND
// at a clean idle boundary (TxIdle). Dead or mid-transaction connections are
// destroyed and a fresh one is dialed on the next Acquire.
type Pool struct {
	cfg  Config
	size int

	mu      sync.Mutex
	idle    []*Conn
	numOpen int
	waiters []chan *Conn
	closed  bool

	// stats
	destroyed int
}

// NewPool creates a pool of at most size connections against cfg.
func NewPool(cfg Config, size int) *Pool {
	if size < 1 {
		size = 1
	}
	return &Pool{cfg: cfg, size: size}
}

// Acquire returns a live connection at a clean boundary, dialing a fresh one if
// needed. It respects ctx cancellation/deadline while waiting for capacity.
func (p *Pool) Acquire(ctx context.Context) (*Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}
	// Reuse an idle live connection.
	for len(p.idle) > 0 {
		c := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if c.IsDead() || c.txStatus != TxIdle {
			p.numOpen--
			p.destroyed++
			_ = c.destroy(nil)
			continue
		}
		p.mu.Unlock()
		return c, nil
	}
	if p.numOpen < p.size {
		p.numOpen++
		p.mu.Unlock()
		c, err := Connect(ctx, p.cfg)
		if err != nil {
			p.mu.Lock()
			p.numOpen--
			p.mu.Unlock()
			return nil, err
		}
		return c, nil
	}
	// At capacity: wait for a release.
	ch := make(chan *Conn, 1)
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()

	select {
	case <-ctx.Done():
		p.mu.Lock()
		// remove our waiter if still queued
		for i, w := range p.waiters {
			if w == ch {
				p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		// a conn may have been handed to us concurrently; return it to the pool
		select {
		case c := <-ch:
			p.Release(c)
		default:
		}
		return nil, ctx.Err()
	case c := <-ch:
		if c == nil {
			return nil, ErrPoolClosed
		}
		if c.IsDead() || c.txStatus != TxIdle {
			// destroy and dial fresh
			p.mu.Lock()
			p.numOpen--
			p.destroyed++
			p.mu.Unlock()
			_ = c.destroy(nil)
			return p.Acquire(ctx)
		}
		return c, nil
	}
}

// Release returns a connection to the pool. A dead or mid-transaction
// connection is destroyed, never pooled; capacity is freed for a fresh dial.
func (p *Pool) Release(c *Conn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		p.numOpen--
		_ = c.destroy(nil)
		return
	}
	if c.IsDead() || c.txStatus != TxIdle || c.CancelTainted() {
		// The load-bearing rule: never pool a poisoned or mid-txn connection.
		// CancelTainted: an out-of-band CancelRequest was fired at this
		// backend; it may land late and kill an unrelated later statement
		// (57014 poisoning), so the conn is destroyed, never reused.
		p.numOpen--
		p.destroyed++
		_ = c.destroy(nil)
		// Wake a waiter so it can dial a fresh connection.
		p.wakeWaiterLocked()
		return
	}
	// Hand directly to a waiter if one exists.
	if len(p.waiters) > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		w <- c
		return
	}
	p.idle = append(p.idle, c)
}

// wakeWaiterLocked signals one waiter (if any) that capacity freed up; the
// waiter path will re-check and dial a fresh connection.
func (p *Pool) wakeWaiterLocked() {
	if len(p.waiters) == 0 {
		return
	}
	// Signal by handing a dead sentinel isn't ideal; instead close the waiter's
	// channel path by delivering nil and let Acquire retry. Simpler: since
	// numOpen was decremented, wake the waiter to re-run Acquire by sending a
	// nil that triggers a fresh dial.
	w := p.waiters[0]
	p.waiters = p.waiters[1:]
	// Reserve a slot for the waiter's fresh dial.
	p.numOpen++
	go func() {
		c, err := Connect(context.Background(), p.cfg)
		if err != nil {
			p.mu.Lock()
			p.numOpen--
			p.mu.Unlock()
			// deliver nil; Acquire waiter returns pool-closed-ish; best effort
			w <- nil
			return
		}
		w <- c
	}()
}

// Stats returns coarse pool counters.
func (p *Pool) Stats() (open, idle, destroyed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.numOpen, len(p.idle), p.destroyed
}

// Close destroys all idle connections and marks the pool closed.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, c := range p.idle {
		_ = c.destroy(nil)
	}
	p.idle = nil
	for _, w := range p.waiters {
		close(w)
	}
	p.waiters = nil
}
