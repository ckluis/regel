package pgwire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

func randSuffix() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// TestMidQueryKillDestroysConn: closing the TCP socket mid-result-set marks the
// conn dead; it is never reusable.
func TestMidQueryKillDestroysConn(t *testing.T) {
	c := mustConnect(t)
	ctx := ctxT(t)
	rows, err := c.Query(ctx, "SELECT g FROM generate_series(1, 100000) g")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("expected at least one row: %v", rows.Err())
	}
	// Kill the socket under the running query.
	c.raw.Close()
	// Continue iterating: must fail and mark the conn dead.
	for rows.Next() {
	}
	if rows.Err() == nil {
		t.Fatal("expected an error after socket kill")
	}
	if !c.IsDead() {
		t.Fatal("conn must be marked dead after mid-query kill")
	}
	if _, err := c.Exec(ctx, "SELECT 1"); err != ErrConnDead && !IsCode(err, "") {
		if !c.IsDead() {
			t.Fatalf("dead conn should reject further use, got %v", err)
		}
	}
}

// TestPoolNeverPoolsDeadConn: a killed connection returned to the pool is
// destroyed, and the next Acquire hands out a fresh live connection.
func TestPoolNeverPoolsDeadConn(t *testing.T) {
	p := NewPool(testConfig(t), 2)
	defer p.Close()
	ctx := ctxT(t)

	c, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Poison it: kill the socket mid-query.
	rows, _ := c.Query(ctx, "SELECT g FROM generate_series(1,100000) g")
	rows.Next()
	c.raw.Close()
	for rows.Next() {
	}
	if !c.IsDead() {
		t.Fatal("conn should be dead")
	}
	p.Release(c) // must destroy, not pool

	_, idle, destroyed := p.Stats()
	if idle != 0 {
		t.Fatalf("dead conn was pooled: idle=%d", idle)
	}
	if destroyed < 1 {
		t.Fatalf("expected destroyed>=1, got %d", destroyed)
	}

	// Next Acquire must dial a fresh, live connection.
	c2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c2.IsDead() {
		t.Fatal("fresh conn should be alive")
	}
	if err := c2.Ping(ctx); err != nil {
		t.Fatalf("fresh conn ping: %v", err)
	}
	p.Release(c2)
}

// TestPoolDestroysMidTxnConn: a connection returned to the pool while still in
// an open transaction is destroyed (not at a clean idle boundary), never
// pooled.
func TestPoolDestroysMidTxnConn(t *testing.T) {
	p := NewPool(testConfig(t), 2)
	defer p.Close()
	ctx := ctxT(t)

	c, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if c.TxStatus() != TxInTx {
		t.Fatalf("expected in-tx, got %c", c.TxStatus())
	}
	p.Release(c) // mid-transaction: must be destroyed

	if !c.IsDead() {
		t.Fatal("mid-txn conn must be destroyed on release")
	}
	_, idle, destroyed := p.Stats()
	if idle != 0 {
		t.Fatalf("mid-txn conn was pooled: idle=%d", idle)
	}
	if destroyed < 1 {
		t.Fatalf("expected destroyed>=1, got %d", destroyed)
	}

	c2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c2.IsDead() {
		t.Fatal("expected fresh live conn")
	}
	p.Release(c2)
}

func TestListenNotify(t *testing.T) {
	listener := mustConnect(t)
	defer listener.Close()
	notifier := mustConnect(t)
	defer notifier.Close()
	ctx := ctxT(t)

	ch := "regel_test_" + randSuffix()
	if err := listener.Listen(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if _, err := notifier.ExecSimple(ctx, "NOTIFY "+quoteIdent(ch)+", 'hello'"); err != nil {
		t.Fatal(err)
	}
	// Trigger the listener to read: a round trip surfaces the async message.
	listener.Ping(ctx)
	select {
	case n := <-listener.Notifications():
		if n.Channel != ch || n.Payload != "hello" {
			t.Fatalf("bad notification %+v", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification received")
	}
}

var _ = context.Background
