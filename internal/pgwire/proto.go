package pgwire

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Backend message type bytes (server -> client). See the Postgres frontend/
// backend protocol v3.0 message formats.
const (
	msgAuthentication  = 'R'
	msgBackendKeyData  = 'K'
	msgBindComplete    = '2'
	msgCloseComplete   = '3'
	msgCommandComplete = 'C'
	msgDataRow         = 'D'
	msgEmptyQuery      = 'I'
	msgErrorResponse   = 'E'
	msgNoData          = 'n'
	msgNoticeResponse  = 'N'
	msgNotification    = 'A'
	msgParameterDesc   = 't'
	msgParameterStatus = 'S'
	msgParseComplete   = '1'
	msgPortalSuspended = 's'
	msgReadyForQuery   = 'Z'
	msgRowDescription  = 'T'
	msgCopyInResponse  = 'G'
	msgCopyOutResponse = 'H'
	msgFunctionResult  = 'V'
)

// Authentication sub-types (the int32 following msgAuthentication).
const (
	authOK                = 0
	authKerberosV5        = 2
	authCleartextPassword = 3
	authMD5Password       = 5
	authSCMCredential     = 6
	authGSS               = 7
	authGSSContinue       = 8
	authSSPI              = 9
	authSASL              = 10
	authSASLContinue      = 11
	authSASLFinal         = 12
)

// backendMsg is a fully-read backend message: a type byte plus its body (the
// length prefix is already consumed).
type backendMsg struct {
	typ  byte
	body []byte
}

// readMessage reads one framed backend message: 1 type byte, a 4-byte
// big-endian length (which includes those 4 bytes), then length-4 body bytes.
func readMessage(r *bufio.Reader) (backendMsg, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return backendMsg{}, err
	}
	length := binary.BigEndian.Uint32(hdr[1:5])
	if length < 4 {
		return backendMsg{}, fmt.Errorf("pgwire: bad message length %d for type %q", length, hdr[0])
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return backendMsg{}, err
	}
	return backendMsg{typ: hdr[0], body: body}, nil
}

// writeBuf accumulates a frontend message body and knows how to frame it.
type writeBuf struct {
	buf []byte
}

func (w *writeBuf) reset()            { w.buf = w.buf[:0] }
func (w *writeBuf) byte(b byte)       { w.buf = append(w.buf, b) }
func (w *writeBuf) bytes(b []byte)    { w.buf = append(w.buf, b...) }
func (w *writeBuf) string(s string)   { w.buf = append(w.buf, s...); w.buf = append(w.buf, 0) }
func (w *writeBuf) int16(v int16) {
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v))
}
func (w *writeBuf) int32(v int32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
}

// frame prepends the type byte and 4-byte length and returns the full message.
// A zero typ means "no type byte" (startup / cancel / SSL request).
func frame(typ byte, body []byte) []byte {
	var out []byte
	length := len(body) + 4
	if typ != 0 {
		out = make([]byte, 0, length+1)
		out = append(out, typ)
	} else {
		out = make([]byte, 0, length)
	}
	out = binary.BigEndian.AppendUint32(out, uint32(length))
	out = append(out, body...)
	return out
}

// reader over a message body for sequential typed reads.
type msgReader struct {
	b   []byte
	pos int
}

func (m *msgReader) int16() int16 {
	v := int16(binary.BigEndian.Uint16(m.b[m.pos:]))
	m.pos += 2
	return v
}

func (m *msgReader) int32() int32 {
	v := int32(binary.BigEndian.Uint32(m.b[m.pos:]))
	m.pos += 4
	return v
}

func (m *msgReader) uint32() uint32 {
	v := binary.BigEndian.Uint32(m.b[m.pos:])
	m.pos += 4
	return v
}

// string reads a NUL-terminated string.
func (m *msgReader) string() string {
	start := m.pos
	for m.pos < len(m.b) && m.b[m.pos] != 0 {
		m.pos++
	}
	s := string(m.b[start:m.pos])
	m.pos++ // skip NUL
	return s
}
