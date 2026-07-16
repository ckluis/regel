package pgwire

import (
	"context"
	"testing"
)

// TestPoolNeverReusesCancelTaintedConn is the regression witness for a
// pooled-conn poisoning observed under whole-suite load (Stage D): armCancel
// fires an out-of-band CancelRequest when a statement's ctx is canceled; the
// cancel can land AFTER the canceled statement already finished, killing the
// NEXT statement executed on that backend with SQLSTATE 57014 ("canceling
// statement due to user request") on a query nobody canceled.
//
// The rule under test: a conn whose cancel ever fired is cancel-tainted and is
// DESTROYED at Release — the pool never hands its backend to anyone again.
func TestPoolNeverReusesCancelTaintedConn(t *testing.T) {
	cfg := testConfig(t)
	pool := NewPool(cfg, 2)
	defer pool.Close()

	ctx := context.Background()
	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	taintedPID := c.backendPID

	// A pre-canceled ctx forces armCancel's watcher to fire the out-of-band
	// CancelRequest (the race's "cancel lands late" arm is inherently timing
	// dependent; the taint rule is what makes it irrelevant).
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = c.ExecSimple(canceled, "SELECT pg_sleep(0)") // error expected; ignore

	if !c.CancelTainted() {
		t.Fatalf("conn not cancel-tainted after ctx-canceled statement")
	}
	pool.Release(c)

	// The tainted backend must never come back out of the pool.
	c2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	defer pool.Release(c2)
	if c2.backendPID == taintedPID {
		t.Fatalf("pool reused cancel-tainted backend PID %d", taintedPID)
	}
	// And the replacement conn is healthy.
	if err := c2.Ping(ctx); err != nil {
		t.Fatalf("fresh conn ping: %v", err)
	}
}
