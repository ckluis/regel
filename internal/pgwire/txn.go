package pgwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
)

// Begin starts a READ COMMITTED transaction.
func (c *Conn) Begin(ctx context.Context) error {
	_, err := c.ExecSimple(ctx, "BEGIN")
	return err
}

// BeginSerializable starts a SERIALIZABLE transaction (the ADR-03 admission and
// ADR-05 step isolation level).
func (c *Conn) BeginSerializable(ctx context.Context) error {
	_, err := c.ExecSimple(ctx, "BEGIN ISOLATION LEVEL SERIALIZABLE")
	return err
}

// Commit commits the current transaction.
func (c *Conn) Commit(ctx context.Context) error {
	_, err := c.ExecSimple(ctx, "COMMIT")
	return err
}

// Rollback rolls back the current transaction. Safe to call after an error.
func (c *Conn) Rollback(ctx context.Context) error {
	if c.dead {
		return c.deadErr
	}
	_, err := c.ExecSimple(ctx, "ROLLBACK")
	return err
}

// InTx reports whether the connection is inside a transaction block.
func (c *Conn) InTx() bool { return c.txStatus != TxIdle }

// armCancel wires context cancellation to an out-of-band CancelRequest. It
// returns a stop function that must be called when the operation completes.
func (c *Conn) armCancel(ctx context.Context) func() {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.sendCancelRequest()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// sendCancelRequest opens a fresh socket and sends a CancelRequest carrying the
// backend PID + secret key. Best-effort: errors are ignored.
func (c *Conn) sendCancelRequest() {
	if c.backendPID == 0 {
		return
	}
	conn, err := net.DialTimeout("tcp", c.cfg.address(), dialCancelTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:4], 16)
	binary.BigEndian.PutUint32(buf[4:8], 80877102) // CancelRequest magic
	binary.BigEndian.PutUint32(buf[8:12], uint32(c.backendPID))
	binary.BigEndian.PutUint32(buf[12:16], uint32(c.secretKey))
	_, _ = conn.Write(buf)
}

// Ping performs a real SELECT 1 round trip at a clean boundary.
func (c *Conn) Ping(ctx context.Context) error {
	var one int64
	ok, err := c.QueryRow(ctx, "SELECT 1", nil, &one)
	if err != nil {
		return err
	}
	if !ok || one != 1 {
		return fmt.Errorf("pgwire: ping returned unexpected result")
	}
	return nil
}
