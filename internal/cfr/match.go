package cfr

import (
	"context"
	"encoding/json"
	"strings"

	"regel.dev/regel/internal/cek"
)

// matchShape is the message-match predicate as stored in the wake jsonb
// (BUILD-D, ADR-05 §5 `match:<pred>`). Minimal v1 form: equality of a dotted
// field Path in the decoded message payload record against a JSON scalar Equals.
// An absent match (Path == "") means the receiver matches any message on its
// channel — the Stage-B FIFO behavior.
type matchShape struct {
	Path   string          `json:"path"`
	Equals json.RawMessage `json:"equals"`
}

// wakeMatchJSON renders a cek.WakeMatch into the `match` sub-object of a message
// wake, or "" when there is no predicate. Only scalar Equals are supported
// (string/number/bool/bigint); a non-scalar collapses to no predicate.
func wakeMatchJSON(m *cek.WakeMatch) string {
	if m == nil || m.Path == "" || !m.Has {
		return ""
	}
	eq, ok := valueToJSONScalar(m.Equals)
	if !ok {
		return ""
	}
	pathJSON, _ := json.Marshal(m.Path)
	return `{"path":` + string(pathJSON) + `,"equals":` + eq + `}`
}

// valueToJSONScalar renders a scalar Value as a JSON literal for the wake jsonb.
func valueToJSONScalar(v cek.Value) (string, bool) {
	if s, ok := v.StrVal(); ok {
		b, _ := json.Marshal(s)
		return string(b), true
	}
	if n, ok := v.Num(); ok {
		b, _ := json.Marshal(n)
		return string(b), true
	}
	if b, ok := v.BoolVal(); ok {
		if b {
			return "true", true
		}
		return "false", true
	}
	if s, ok := v.BigStr(); ok {
		b, _ := json.Marshal(s)
		return string(b), true
	}
	return "", false
}

// messageMatches reports whether a decoded message payload satisfies a stored
// match predicate. An empty predicate (or a predicate whose path is absent from
// the payload) is treated as: empty ⇒ match anything; present-but-missing ⇒ no
// match. Equality is by normalized scalar value.
func messageMatches(payload cek.Value, m matchShape) bool {
	if m.Path == "" {
		return true
	}
	field, ok := navigatePath(payload, m.Path)
	if !ok {
		return false
	}
	got, ok := scalarOf(field)
	if !ok {
		return false
	}
	var want any
	if err := json.Unmarshal(m.Equals, &want); err != nil {
		return false
	}
	return scalarEqual(got, want)
}

// navigatePath walks a dotted field path into a record Value.
func navigatePath(v cek.Value, path string) (cek.Value, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		f, ok := cur.Field(seg)
		if !ok {
			return cek.Value{}, false
		}
		cur = f
	}
	return cur, true
}

// scalarOf projects a scalar Value to a comparable Go value.
func scalarOf(v cek.Value) (any, bool) {
	if s, ok := v.StrVal(); ok {
		return s, true
	}
	if n, ok := v.Num(); ok {
		return n, true
	}
	if b, ok := v.BoolVal(); ok {
		return b, true
	}
	if s, ok := v.BigStr(); ok {
		return s, true
	}
	return nil, false
}

// scalarEqual compares two decoded scalars, tolerating JSON's float/int fuzz.
func scalarEqual(got, want any) bool {
	switch g := got.(type) {
	case string:
		w, ok := want.(string)
		return ok && g == w
	case float64:
		w, ok := want.(float64)
		return ok && g == w
	case bool:
		w, ok := want.(bool)
		return ok && g == w
	default:
		return false
	}
}

// --- event wakes (BUILD-D, ADR-05 §5) ----------------------------------------

// WakeEvents flips every event-parked continuation whose stream == resource and
// whose watch set includes rowID (or is empty ⇒ ANY row) to 'ready', inserting a
// resume task for each — all inside the caller's OPEN transaction, so the wake
// commits atomically with the triggering mutation (ADR-05 §5: "message/event
// wakes are flipped to ready in the same transaction as the triggering write").
// It returns the number of continuations woken. Idempotent under the status CAS.
func WakeEvents(ctx context.Context, db DB, resource, rowID string) (int, error) {
	rows, err := db.Query(ctx, `
SELECT id::text, step_seq, COALESCE(wake->'on','[]'::jsonb)::text
FROM continuation
WHERE status='sleeping' AND wake->>'kind'='event' AND wake->>'stream'=$1`, resource)
	if err != nil {
		return 0, err
	}
	type cand struct {
		id   string
		seq  int64
		onJS string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.seq, &c.onJS); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	woken := 0
	for _, c := range cands {
		var on []string
		_ = json.Unmarshal([]byte(c.onJS), &on)
		if len(on) > 0 && !contains(on, rowID) {
			continue // watching specific rows, this one is not among them
		}
		res, err := db.Exec(ctx, `
UPDATE continuation SET status='ready', updated_at=now()
 WHERE id=$1 AND status='sleeping'`, c.id)
		if err != nil {
			return woken, err
		}
		if res.RowsAffected != 1 {
			continue // lost to a concurrent flip — idempotent
		}
		if err := insertResumeTask(ctx, db, c.id, c.seq); err != nil {
			return woken, err
		}
		woken++
	}
	if woken > 0 {
		if err := notifyTask(ctx, db); err != nil {
			return woken, err
		}
	}
	return woken, nil
}

// nullableJSON maps "" to SQL NULL, else the JSON literal string. Used with
// jsonb_strip_nulls so an absent match predicate leaves no `match` key.
func nullableJSON(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// claimOldestMatchingMessage claims the oldest undelivered message on channel
// whose payload satisfies the receiver's match predicate (BUILD-D, ADR-05 §5
// send-before-receive), assigning it to receiverID. Returns the claimed message
// id, or "" when none matches (the receiver then parks). Non-matching messages
// are left undelivered for other receivers.
func claimOldestMatchingMessage(ctx context.Context, db DB, channel string, m *cek.WakeMatch, receiverID string) (string, error) {
	var pred matchShape
	if m != nil && m.Path != "" && m.Has {
		if eq, ok := valueToJSONScalar(m.Equals); ok {
			pred = matchShape{Path: m.Path, Equals: json.RawMessage(eq)}
		}
	}
	rows, err := db.Query(ctx, `
SELECT id::text, encode(payload,'hex') FROM channel_message
WHERE channel=$1 AND claimed_by IS NULL ORDER BY sent_at`, channel)
	if err != nil {
		return "", err
	}
	type msgCand struct {
		id     string
		payHex string
	}
	var cands []msgCand
	for rows.Next() {
		var c msgCand
		if err := rows.Scan(&c.id, &c.payHex); err != nil {
			rows.Close()
			return "", err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, c := range cands {
		if pred.Path != "" {
			blob, err := hexDecode(c.payHex)
			if err != nil {
				return "", err
			}
			payload, err := DecodeValue(blob)
			if err != nil {
				return "", err
			}
			if !messageMatches(payload, pred) {
				continue
			}
		}
		res, err := db.Exec(ctx, `
UPDATE channel_message SET claimed_by=$1 WHERE id=$2 AND claimed_by IS NULL`, receiverID, c.id)
		if err != nil {
			return "", err
		}
		if res.RowsAffected != 1 {
			continue // lost to a concurrent claim
		}
		return c.id, nil
	}
	return "", nil
}
