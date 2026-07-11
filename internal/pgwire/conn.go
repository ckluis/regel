package pgwire

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const protocolVersion30 = 196608 // 3 << 16

// Notification is an async NOTIFY delivered at a clean message boundary.
type Notification struct {
	PID     int32
	Channel string
	Payload string
}

// TxStatus mirrors the ReadyForQuery status byte.
type TxStatus byte

const (
	TxIdle     TxStatus = 'I' // not in a transaction
	TxInTx     TxStatus = 'T' // in a transaction block
	TxInFailed TxStatus = 'E' // in a failed transaction block
)

// Conn is a single owned Postgres connection. Not safe for concurrent use by
// multiple goroutines (except Notifications, which is a receive-only channel).
type Conn struct {
	cfg  Config
	raw  net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	wbuf writeBuf

	backendPID int32
	secretKey  int32

	txStatus TxStatus
	dead     bool
	deadErr  error

	// prepared statement cache keyed by SQL text -> backend statement name.
	prepared map[string]string
	stmtSeq  int

	notifyCh chan Notification

	mu sync.Mutex // guards writes to raw during out-of-band cancel
}

// Connect dials and performs startup + authentication.
func Connect(ctx context.Context, cfg Config) (*Conn, error) {
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", cfg.address())
	if err != nil {
		return nil, fmt.Errorf("pgwire: dial %s: %w", cfg.address(), err)
	}
	c := &Conn{
		cfg:      cfg,
		raw:      raw,
		r:        bufio.NewReader(raw),
		w:        bufio.NewWriter(raw),
		prepared: map[string]string{},
		notifyCh: make(chan Notification, 32),
		txStatus: TxIdle,
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	if cfg.TLS {
		if err := c.negotiateTLS(cfg); err != nil {
			raw.Close()
			return nil, err
		}
	}
	if err := c.startup(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{}) // clear connect deadline
	return c, nil
}

// negotiateTLS sends an SSLRequest and upgrades the socket if the server
// answers 'S'. This is best-effort plumbing; on a trust/local box TLS is off.
func (c *Conn) negotiateTLS(cfg Config) error {
	// SSLRequest: length 8, magic 80877103.
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:4], 8)
	binary.BigEndian.PutUint32(buf[4:8], 80877103)
	if _, err := c.raw.Write(buf); err != nil {
		return err
	}
	var resp [1]byte
	if _, err := c.raw.Read(resp[:]); err != nil {
		return err
	}
	if resp[0] != 'S' {
		return fmt.Errorf("pgwire: server refused TLS")
	}
	tlsConn := tls.Client(c.raw, &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		return err
	}
	c.raw = tlsConn
	c.r = bufio.NewReader(tlsConn)
	c.w = bufio.NewWriter(tlsConn)
	return nil
}

func (c *Conn) startup(ctx context.Context) error {
	w := &writeBuf{}
	w.int32(protocolVersion30)
	w.string("user")
	w.string(c.cfg.User)
	w.string("database")
	w.string(c.cfg.Database)
	for k, v := range c.cfg.RuntimeParams {
		w.string(k)
		w.string(v)
	}
	w.byte(0) // terminating empty string
	if err := c.writeRaw(frame(0, w.buf)); err != nil {
		return err
	}
	// Authentication loop.
	for {
		m, err := c.readMsg()
		if err != nil {
			return err
		}
		switch m.typ {
		case msgAuthentication:
			if err := c.handleAuth(m.body); err != nil {
				return err
			}
		case msgBackendKeyData:
			mr := msgReader{b: m.body}
			c.backendPID = mr.int32()
			c.secretKey = mr.int32()
		case msgParameterStatus:
			// ignore server GUCs at startup
		case msgNoticeResponse:
			// ignore notices at startup
		case msgReadyForQuery:
			c.txStatus = TxStatus(m.body[0])
			return nil
		case msgErrorResponse:
			return parseErrorResponse(m.body)
		default:
			return c.destroy(fmt.Errorf("pgwire: unexpected message %q during startup", m.typ))
		}
	}
}

func (c *Conn) handleAuth(body []byte) error {
	mr := msgReader{b: body}
	code := mr.int32()
	switch code {
	case authOK:
		return nil
	case authCleartextPassword:
		w := &writeBuf{}
		w.string(c.cfg.Password)
		return c.writeMsg('p', w.buf)
	case authSASL:
		return c.authSCRAM(body[4:])
	case authMD5Password:
		return c.destroy(fmt.Errorf("pgwire: MD5 auth not supported (SCRAM/trust only)"))
	default:
		return c.destroy(fmt.Errorf("pgwire: unsupported auth method %d", code))
	}
}

// authSCRAM runs SCRAM-SHA-256. mechListBody is the AuthenticationSASL payload
// after the int32 code: a NUL-terminated mechanism list.
func (c *Conn) authSCRAM(mechListBody []byte) error {
	mechs := map[string]bool{}
	mr := msgReader{b: mechListBody}
	for {
		name := mr.string()
		if name == "" {
			break
		}
		mechs[name] = true
	}
	if !mechs["SCRAM-SHA-256"] {
		return c.destroy(fmt.Errorf("pgwire: server offered no supported SASL mechanism"))
	}
	sc, err := newSCRAMClient(c.cfg.Password)
	if err != nil {
		return c.destroy(err)
	}
	// SASLInitialResponse
	first := sc.clientFirst()
	w := &writeBuf{}
	w.string("SCRAM-SHA-256")
	w.int32(int32(len(first)))
	w.bytes([]byte(first))
	if err := c.writeMsg('p', w.buf); err != nil {
		return err
	}
	// AuthenticationSASLContinue
	m, err := c.readMsg()
	if err != nil {
		return err
	}
	if m.typ == msgErrorResponse {
		return parseErrorResponse(m.body)
	}
	if m.typ != msgAuthentication {
		return c.destroy(fmt.Errorf("pgwire: expected SASLContinue, got %q", m.typ))
	}
	cmr := msgReader{b: m.body}
	if cmr.int32() != authSASLContinue {
		return c.destroy(fmt.Errorf("pgwire: expected SASLContinue code"))
	}
	serverFirst := string(m.body[4:])
	final, err := sc.clientFinal(serverFirst)
	if err != nil {
		return c.destroy(err)
	}
	// SASLResponse
	if err := c.writeMsg('p', []byte(final)); err != nil {
		return err
	}
	// AuthenticationSASLFinal
	m, err = c.readMsg()
	if err != nil {
		return err
	}
	if m.typ == msgErrorResponse {
		return parseErrorResponse(m.body)
	}
	if m.typ != msgAuthentication {
		return c.destroy(fmt.Errorf("pgwire: expected SASLFinal, got %q", m.typ))
	}
	fmr := msgReader{b: m.body}
	if fmr.int32() != authSASLFinal {
		return c.destroy(fmt.Errorf("pgwire: expected SASLFinal code"))
	}
	if err := sc.verifyServerFinal(string(m.body[4:])); err != nil {
		return c.destroy(err)
	}
	return nil
}

// --- low-level IO -----------------------------------------------------------

func (c *Conn) writeRaw(b []byte) error {
	if _, err := c.w.Write(b); err != nil {
		return c.destroy(err)
	}
	if err := c.w.Flush(); err != nil {
		return c.destroy(err)
	}
	return nil
}

func (c *Conn) writeMsg(typ byte, body []byte) error {
	return c.writeRaw(frame(typ, body))
}

// readMsg reads one backend message, marking the conn dead on any IO error.
func (c *Conn) readMsg() (backendMsg, error) {
	if c.dead {
		return backendMsg{}, c.deadErr
	}
	m, err := readMessage(c.r)
	if err != nil {
		return backendMsg{}, c.destroy(err)
	}
	return m, nil
}

// readMsgAsync reads the next message, transparently consuming async
// ParameterStatus / NoticeResponse / NotificationResponse messages.
func (c *Conn) readMsgAsync() (backendMsg, error) {
	for {
		m, err := c.readMsg()
		if err != nil {
			return backendMsg{}, err
		}
		switch m.typ {
		case msgParameterStatus, msgNoticeResponse:
			continue
		case msgNotification:
			c.deliverNotification(m.body)
			continue
		default:
			return m, nil
		}
	}
}

func (c *Conn) deliverNotification(body []byte) {
	mr := msgReader{b: body}
	n := Notification{PID: mr.int32(), Channel: mr.string(), Payload: mr.string()}
	select {
	case c.notifyCh <- n:
	default: // drop if the consumer is not keeping up
	}
}

// destroy marks the connection dead and closes the socket. The
// destroy-on-desync rule: any protocol/IO fault makes the conn unusable and
// unpoolable. Returns the wrapped error for convenient chaining.
func (c *Conn) destroy(cause error) error {
	if !c.dead {
		c.dead = true
		if cause != nil {
			c.deadErr = fmt.Errorf("%w: %v", ErrConnDead, cause)
		} else {
			c.deadErr = ErrConnDead
		}
		if c.raw != nil {
			_ = c.raw.Close()
		}
	}
	return c.deadErr
}

// IsDead reports whether the connection has been destroyed.
func (c *Conn) IsDead() bool { return c.dead }

// TxStatus returns the last-observed transaction status.
func (c *Conn) TxStatus() TxStatus { return c.txStatus }

// Notifications returns the channel of async NOTIFY messages.
func (c *Conn) Notifications() <-chan Notification { return c.notifyCh }

// Close cleanly terminates the connection (sends Terminate) unless already dead.
func (c *Conn) Close() error {
	if c.dead {
		return nil
	}
	_ = c.writeMsg('X', nil)
	c.dead = true
	c.deadErr = ErrConnDead
	return c.raw.Close()
}

// --- simple query protocol --------------------------------------------------

// ExecSimple runs one or more statements via the simple query protocol. It is
// the path for multi-statement DDL scripts. Returns the last CommandComplete.
func (c *Conn) ExecSimple(ctx context.Context, sql string) (Result, error) {
	if c.dead {
		return Result{}, c.deadErr
	}
	cancel := c.armCancel(ctx)
	defer cancel()

	w := &writeBuf{}
	w.string(sql)
	if err := c.writeMsg('Q', w.buf); err != nil {
		return Result{}, err
	}
	var last Result
	var firstErr error
	for {
		m, err := c.readMsgAsync()
		if err != nil {
			return Result{}, err
		}
		switch m.typ {
		case msgRowDescription, msgDataRow, msgEmptyQuery:
			// discard rows from simple-protocol multi-statement scripts
		case msgCommandComplete:
			last = parseCommandComplete(m.body)
		case msgErrorResponse:
			if firstErr == nil {
				firstErr = parseErrorResponse(m.body)
			}
		case msgReadyForQuery:
			c.txStatus = TxStatus(m.body[0])
			if firstErr != nil {
				return Result{}, firstErr
			}
			return last, nil
		case msgCopyInResponse, msgCopyOutResponse:
			return Result{}, c.destroy(fmt.Errorf("pgwire: COPY not supported"))
		default:
			return Result{}, c.destroy(fmt.Errorf("pgwire: unexpected message %q in simple query", m.typ))
		}
	}
}

func parseCommandComplete(body []byte) Result {
	tag := string(body)
	if n := len(tag); n > 0 && tag[n-1] == 0 {
		tag = tag[:n-1]
	}
	r := Result{Tag: tag}
	// Last space-separated field is the row count for INSERT/UPDATE/DELETE/etc.
	fields := strings.Fields(tag)
	if len(fields) >= 2 {
		if v, err := strconv.ParseInt(fields[len(fields)-1], 10, 64); err == nil {
			r.RowsAffected = v
		}
	}
	return r
}

// --- extended query protocol ------------------------------------------------

// Exec runs a single statement with bound parameters and no returned rows
// (rows are discarded). Uses the extended protocol + prepared-statement cache.
func (c *Conn) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return rows.Result(), nil
}

// Query runs a single statement with bound parameters and returns a cursor.
// The returned *Rows must be fully iterated (or the connection is left dirty
// and will be destroyed on return to the pool).
func (c *Conn) Query(ctx context.Context, sql string, args ...any) (*Rows, error) {
	if c.dead {
		return nil, c.deadErr
	}
	cancel := c.armCancel(ctx)

	stmtName, parsed := c.prepared[sql]
	sendParse := !parsed
	if sendParse {
		c.stmtSeq++
		stmtName = "s" + strconv.Itoa(c.stmtSeq)
	}

	params, err := encodeArgs(args)
	if err != nil {
		cancel()
		return nil, err
	}

	var out []byte
	if sendParse {
		pw := &writeBuf{}
		pw.string(stmtName)
		pw.string(sql)
		pw.int16(0) // let server infer all param types
		out = append(out, frame('P', pw.buf)...)
	}
	// Bind
	bw := &writeBuf{}
	bw.string("") // unnamed portal
	bw.string(stmtName)
	bw.int16(int16(len(params))) // param format codes: one 0 (text) each
	for range params {
		bw.int16(0)
	}
	bw.int16(int16(len(params)))
	for _, p := range params {
		if p == nil {
			bw.int32(-1)
		} else {
			bw.int32(int32(len(p)))
			bw.bytes(p)
		}
	}
	bw.int16(1) // result format codes: single 0 => all text
	bw.int16(0)
	out = append(out, frame('B', bw.buf)...)
	// Describe portal
	dw := &writeBuf{}
	dw.byte('P')
	dw.string("")
	out = append(out, frame('D', dw.buf)...)
	// Execute (unlimited rows)
	ew := &writeBuf{}
	ew.string("")
	ew.int32(0)
	out = append(out, frame('E', ew.buf)...)
	// Sync
	out = append(out, frame('S', nil)...)

	if err := c.writeRaw(out); err != nil {
		cancel()
		return nil, err
	}

	rows := &Rows{conn: c}
	rows.cancel = cancel

	// Read up to (and including) RowDescription/NoData; DataRows stream lazily.
	if sendParse {
		if err := c.expect(msgParseComplete); err != nil {
			cancel()
			return nil, err
		}
		c.prepared[sql] = stmtName
	}
	if err := c.expect(msgBindComplete); err != nil {
		cancel()
		return nil, err
	}
	m, err := c.readMsgAsync()
	if err != nil {
		cancel()
		return nil, err
	}
	switch m.typ {
	case msgRowDescription:
		rows.fields = parseRowDescription(m.body)
	case msgNoData:
		rows.fields = nil
	case msgErrorResponse:
		pe := parseErrorResponse(m.body)
		c.drainToReady()
		cancel()
		return nil, pe
	default:
		cancel()
		return nil, c.destroy(fmt.Errorf("pgwire: unexpected message %q after Bind", m.typ))
	}
	return rows, nil
}

// QueryRow runs a query expected to yield at most one row and scans it. Returns
// (false, nil) when no row is present.
func (c *Conn) QueryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error) {
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	got := false
	if rows.Next() {
		got = true
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return false, err
		}
	}
	for rows.Next() {
	}
	return got, rows.Err()
}

func (c *Conn) expect(typ byte) error {
	m, err := c.readMsgAsync()
	if err != nil {
		return err
	}
	if m.typ == msgErrorResponse {
		pe := parseErrorResponse(m.body)
		c.drainToReady()
		return pe
	}
	if m.typ != typ {
		return c.destroy(fmt.Errorf("pgwire: expected message %q, got %q", typ, m.typ))
	}
	return nil
}

// drainToReady reads messages until ReadyForQuery after an error, restoring the
// connection to a clean boundary (so it CAN be pooled after a query error).
func (c *Conn) drainToReady() {
	for {
		m, err := c.readMsg()
		if err != nil {
			return // already destroyed
		}
		if m.typ == msgReadyForQuery {
			c.txStatus = TxStatus(m.body[0])
			return
		}
	}
}

func parseRowDescription(body []byte) []fieldDesc {
	mr := msgReader{b: body}
	n := int(mr.int16())
	fields := make([]fieldDesc, n)
	for i := 0; i < n; i++ {
		fields[i] = fieldDesc{
			name:     mr.string(),
			tableOID: mr.uint32(),
			colAttr:  mr.int16(),
			typeOID:  mr.uint32(),
			typeLen:  mr.int16(),
			typeMod:  mr.int32(),
			format:   mr.int16(),
		}
	}
	return fields
}

// encodeArgs renders Go values to text-format parameter bytes (nil == NULL).
func encodeArgs(args []any) ([][]byte, error) {
	out := make([][]byte, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case nil:
			out[i] = nil
		case string:
			out[i] = []byte(v)
		case []byte:
			out[i] = v
		case int:
			out[i] = []byte(strconv.FormatInt(int64(v), 10))
		case int64:
			out[i] = []byte(strconv.FormatInt(v, 10))
		case int32:
			out[i] = []byte(strconv.FormatInt(int64(v), 10))
		case float64:
			out[i] = []byte(strconv.FormatFloat(v, 'g', -1, 64))
		case bool:
			if v {
				out[i] = []byte("t")
			} else {
				out[i] = []byte("f")
			}
		case time.Time:
			out[i] = []byte(v.Format("2006-01-02 15:04:05.999999999Z07:00"))
		case []string:
			out[i] = []byte(encodeTextArray(v))
		default:
			return nil, fmt.Errorf("pgwire: unsupported arg type %T at index %d", a, i)
		}
	}
	return out, nil
}

// --- Rows iteration ---------------------------------------------------------

// Next advances to the next row. Returns false at end (check Err).
func (r *Rows) Next() bool {
	if r.done || r.err != nil {
		r.vals = nil
		return false
	}
	m, err := r.conn.readMsgAsync()
	if err != nil {
		r.err = err
		r.vals = nil
		return false
	}
	switch m.typ {
	case msgDataRow:
		r.vals = parseDataRow(m.body)
		if !validateUTF8(r.vals, r.fields) {
			r.err = r.conn.destroy(fmt.Errorf("pgwire: invalid UTF-8 in text result"))
			r.vals = nil
			return false
		}
		return true
	case msgCommandComplete:
		r.result = parseCommandComplete(m.body)
		r.finish()
		return false
	case msgEmptyQuery:
		r.finish()
		return false
	case msgErrorResponse:
		r.err = parseErrorResponse(m.body)
		r.conn.drainToReady()
		if r.cancel != nil {
			r.cancel()
		}
		return false
	case msgPortalSuspended:
		// unlimited execute means this should not happen; treat as done
		r.finish()
		return false
	default:
		r.err = r.conn.destroy(fmt.Errorf("pgwire: unexpected message %q during row iteration", m.typ))
		return false
	}
}

func (r *Rows) finish() {
	r.done = true
	r.vals = nil
	r.conn.drainToReady()
	if r.cancel != nil {
		r.cancel()
	}
}

// Close drains any remaining rows so the connection returns to a clean
// boundary. Safe to call multiple times.
func (r *Rows) Close() {
	for r.Next() {
	}
}

func parseDataRow(body []byte) [][]byte {
	mr := msgReader{b: body}
	n := int(mr.int16())
	vals := make([][]byte, n)
	for i := 0; i < n; i++ {
		length := mr.int32()
		if length == -1 {
			vals[i] = nil
			continue
		}
		vals[i] = mr.b[mr.pos : mr.pos+int(length)]
		mr.pos += int(length)
	}
	return vals
}

func validateUTF8(vals [][]byte, fields []fieldDesc) bool {
	for i, v := range vals {
		if v == nil {
			continue
		}
		// bytea (OID 17) is not text; skip. Everything else in v1 is text.
		if i < len(fields) && fields[i].typeOID == 17 {
			continue
		}
		if !utf8.Valid(v) {
			return false
		}
	}
	return true
}
