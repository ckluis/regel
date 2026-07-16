package kernel

// session_step.go is the ADR-11 §5 session event loop, composed over
// cfr.ClaimAndStep (never forked). A session-specific resume closure re-reads the
// subscribed resources through the erf read path, applies the delivered event or
// invalidation (a form mutation goes through the validated + rowVersion-guarded
// path), diffs against the last-sent snapshot, and stashes the patch frame. The
// checkpoint re-parks the row on its message channel and rewrites the subscription
// set — one step transaction. INVARIANT (§2): every checkpoint that advances
// step_seq emits exactly one frame; a UI-local/no-change event emits the zero-op
// frame [eventSeq, snapshotHash, []].

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// sessionMsg is one delivered event or invalidation (ADR-05 one-wake message).
type sessionMsg struct {
	Kind   string // "event" | "invalidate"
	SlotID string
	Event  string // "input" | "blur" | "submit" | "click"
	Value  string
}

// stepFaultHook is a TEST-ONLY seam (default nil): a non-nil hook run AFTER the
// mutation inside the session step. It may poison the open transaction and return
// an error to simulate a kernel death between claim and checkpoint — the whole
// SERIALIZABLE step then rolls back (mutation included), no frame, no step_seq
// advance commits, and the next event resumes from the prior checkpoint.
var stepFaultHook func(ctx context.Context, conn *pgwire.Conn, sessionID string) error

// armSession flips a message-parked session (sleeping/ready at step_seq=atSeq) to
// 'ready' so ClaimAndStep can claim it — the session analogue of a channel wake.
// Idempotent: a double-fired event whose step_seq already advanced (or that finds
// the row running/checkpointed) arms zero rows and is dropped (§5 double-event).
func (s *Server) armSession(ctx context.Context, conn *pgwire.Conn, sessionID string, atSeq int64) (bool, error) {
	res, err := conn.Exec(ctx, `
UPDATE continuation SET status='ready', updated_at=now()
 WHERE id=$1 AND kind='session' AND status IN ('sleeping','ready') AND step_seq=$2`,
		sessionID, atSeq)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// driveSession arms the session at atSeq and drives exactly one step through
// ClaimAndStep, applying msg. It returns the produced frame (post-commit), whether
// the claim won, and any error. A dropped double-fire returns (nil, false, nil).
// The frame is pushed to the SSE ring only AFTER the step transaction commits.
func (s *Server) driveSession(ctx context.Context, sessionID string, atSeq int64, msg sessionMsg) (*ui.Frame, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer s.pool.Release(conn)

	armed, err := s.armSession(ctx, conn, sessionID, atSeq)
	if err != nil {
		return nil, false, err
	}
	if !armed {
		return nil, false, nil // double-fire / stale — dropped idempotently
	}

	var frame *ui.Frame
	var resumeErr error
	resume := func(state *cek.State, _ cek.Delivery, _ cek.Principal) cek.Outcome {
		out, f, e := s.runSessionStep(ctx, conn, sessionID, atSeq, state, msg)
		if e != nil {
			resumeErr = e
			return cek.Outcome{Kind: cek.OutError, Err: e}
		}
		frame = f
		return out
	}

	// The session step is one SERIALIZABLE transaction (ClaimAndStep). A 40001
	// raised INSIDE the resume closure aborts the txn and surfaces to us as a masked
	// 25P02 (the checkpoint's parkFailed writes hit the aborted txn), which
	// cfr.RetrySerializable does not recognize — so we own the retry here, keying on
	// the serialization class OR the resume's captured 40001. A rolled-back step
	// reverts the row to its armed (ready, step_seq=atSeq) state, so the retry re-claims.
	var claimed bool
	var stepErr error
	for attempt := 0; attempt < 12; attempt++ {
		frame, resumeErr = nil, nil
		_, c, e := cfr.ClaimAndStep(ctx, conn, s.stepEnv(30), s.interp, sessionID, atSeq, resume)
		claimed = c
		stepErr = e
		if e == nil {
			break
		}
		if !sessionRetryable(e) && !sessionRetryable(resumeErr) {
			return nil, claimed, e
		}
		sessionBackoff(ctx, attempt)
	}
	if stepErr != nil {
		return nil, claimed, stepErr
	}
	if !claimed || frame == nil {
		return nil, claimed, nil
	}
	// Publish only after commit (§5): the frame's eventSeq == the committed step_seq.
	s.sse().push(sessionID, *frame)
	incFramesSent()
	return frame, true, nil
}

// runSessionStep is the body of the session resume: apply msg, re-render, diff,
// build the frame, rewrite subscriptions, and re-park on the message channel. It
// runs inside ClaimAndStep's open transaction on conn.
func (s *Server) runSessionStep(ctx context.Context, conn *pgwire.Conn, sessionID string, atSeq int64, state *cek.State, msg sessionMsg) (cek.Outcome, *ui.Frame, error) {
	sess, err := sessionFromState(state)
	if err != nil {
		return cek.Outcome{}, nil, err
	}
	vm, err := loadViewMeta(ctx, conn, sess.Resource)
	if err != nil {
		return cek.Outcome{}, nil, err
	}
	mc, err := admission.BuildMaskCtx(ctx, conn, sess.Principal)
	if err != nil {
		return cek.Outcome{}, nil, err
	}
	if err := s.applyMsg(ctx, conn, vm, sess, msg); err != nil {
		return cek.Outcome{}, nil, err
	}
	if stepFaultHook != nil {
		if e := stepFaultHook(ctx, conn, sessionID); e != nil {
			return cek.Outcome{}, nil, e // simulated death: the whole step rolls back
		}
	}

	_, next, subs, _, rows, err := renderView(ctx, conn, vm, sess, mc)
	if err != nil {
		return cek.Outcome{}, nil, err
	}
	t := vm.template(sess.Kind)
	last := matFromSnap(sess.LastSnap)
	ops := ui.Diff(t, last, next)
	if sess.Kind == "table" {
		lastRows := listRowsFromKeys(sess.RowKeys)
		nextRows := make([]ui.ListRow, len(rows))
		for i, rd := range rows {
			h, _ := ui.RenderRow(t, vm.Table, rd, mc)
			nextRows[i] = ui.ListRow{Key: rd.Key, HTML: h}
		}
		if sp := ui.DiffList(t.Slots[0].ID, lastRows, nextRows); sp != nil {
			ops = append(ops, *sp)
		}
		sess.RowKeys = keysOfRows(rows)
	}
	// Diff keys on the SNAPSHOT map (masked tokens) so a grant flip/expiry still
	// diffs (§8); the WIRE snapshotHash is summed over the DISPLAY map — the values
	// the client actually holds (plaintext only under a live grant) — so client and
	// server divergence digests are over the same bytes (§4).
	newSnap := snapOf(next)
	dig := ui.FullDigest(displayMapOf(next))
	frame := &ui.Frame{EventSeq: uint64(atSeq + 1), SnapshotHash: uint64(dig), Ops: ops}

	sess.LastSnap = newSnap
	sess.setDigest(dig)
	// NB: sess.RowVersion is the version the form was OPENED against — it is set at
	// mount and advanced ONLY by submitForm (success ⇒ base+1, conflict ⇒ reloaded
	// current), never by an ordinary re-render, so optimistic concurrency holds.

	// On an INVALIDATION the mount expression is unchanged, so the read-set — and
	// thus the subscription set — is identical to what is already stored; rewriting
	// it would only add SSI write-contention on the shared (resource,key) index under
	// a fan-out storm. Rewrite subscriptions only on an EVENT (which may navigate).
	if msg.Kind != "invalidate" {
		if err := writeSubscriptions(ctx, conn, sessionID, subs); err != nil {
			return cek.Outcome{}, nil, err
		}
	}
	if err := s.checkSizeCap(sess); err != nil {
		return cek.Outcome{}, nil, err
	}

	out := cek.Outcome{
		Kind:  cek.OutParked,
		State: sess.toState(),
		Wake:  &cek.Wake{Kind: cek.WakeMessage, Channel: sessionID},
	}
	return out, frame, nil
}

// sessionRetryable reports whether err is a serialization-class abort worth
// retrying the whole session step: 40001 (serialization), 40P01 (deadlock), or
// 25P02 (a transaction aborted by an earlier — masked — 40001 inside the resume).
func sessionRetryable(err error) bool {
	if err == nil {
		return false
	}
	return pgwire.IsCode(err, "40001") || pgwire.IsCode(err, "40P01") || pgwire.IsCode(err, "25P02")
}

// sessionBackoff sleeps a small jittered backoff between step retries.
func sessionBackoff(ctx context.Context, attempt int) {
	d := time.Duration(2<<uint(min(attempt, 6))) * time.Millisecond
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// --- message application (events + mutations) ---------------------------------

func (s *Server) applyMsg(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, sess *sessionCFR, msg sessionMsg) error {
	if msg.Kind == "invalidate" {
		return nil // a dependency changed — re-render is the whole job
	}
	// msg.Kind == "event"
	switch msg.Event {
	case "input", "blur":
		if field := fieldForSlot(vm, "form", msg.SlotID); field != "" && field != "__alert__" {
			sess.Draft[field] = msg.Value
		}
		sess.UILocal["__alert__"] = "" // typing clears a prior alert
		return nil
	case "click":
		if msg.SlotID != "" {
			sess.UILocal[msg.SlotID] = msg.Value // UI-local toggle (open dialog, etc.)
		}
		return nil
	case "submit":
		return s.submitForm(ctx, conn, vm, sess)
	default:
		return nil
	}
}

// submitForm runs server-authoritative validation (§7), then the rowVersion-guarded
// mutation. A validation failure sets the alert and keeps the draft (no write). A
// zero-row guard is a concurrent-edit conflict: reload the current values, keep the
// draft in unsaved fields, and alert. A success commits, clears the draft, and
// NOTIFYs the dependency-exact invalidation (§6).
func (s *Server) submitForm(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, sess *sessionCFR) error {
	if sess.RowID == "" {
		sess.UILocal["__alert__"] = "create is not supported in this view"
		return nil
	}
	// Validate every drafted field.
	fields := draftFieldsSorted(sess)
	for _, field := range fields {
		f := findField(vm, field)
		if f == nil {
			continue
		}
		if reason := validateField(*f, sess.Draft[field]); reason != "" {
			sess.UILocal["__alert__"] = "please fix " + field + ": " + reason
			return nil
		}
	}
	// pii drafts route to the vault; non-pii drafts build the guarded UPDATE.
	var sets []string
	var args []any
	i := 1
	for _, field := range fields {
		f := findField(vm, field)
		if f == nil {
			continue
		}
		if f.PII {
			if err := admission.VaultPut(ctx, conn, vm.Table, sess.RowID, field, sess.Draft[field]); err != nil {
				return err
			}
			continue
		}
		col := displayColumn(*f)
		if col == "" {
			continue
		}
		sets = append(sets, quoteIdent(col)+"=$"+strconv.Itoa(i))
		args = append(args, sess.Draft[field])
		i++
	}
	base, _ := strconv.ParseInt(sess.RowVersion, 10, 64)
	if len(sets) > 0 {
		sets = append(sets, "row_version=row_version+1")
		sqlStr := "UPDATE " + quoteIdent(vm.Table) + " SET " + strings.Join(sets, ", ") +
			" WHERE id=$" + strconv.Itoa(i) + " AND row_version=$" + strconv.Itoa(i+1)
		args = append(args, sess.RowID, base)
		res, err := conn.Exec(ctx, sqlStr, args...)
		if err != nil {
			return err
		}
		if res.RowsAffected == 0 {
			// Concurrent edit (ADR-11 §7): reject-and-reconcile. Draft preserved; the
			// form is re-based on the CURRENT row_version so a reviewed resubmit lands.
			sess.UILocal["__alert__"] = "this record changed — review and resubmit"
			var cur int64
			_, _ = conn.QueryRow(ctx,
				`SELECT coalesce(row_version,0) FROM `+quoteIdent(vm.Table)+` WHERE id=$1`,
				[]any{sess.RowID}, &cur)
			sess.RowVersion = strconv.FormatInt(cur, 10)
			return nil
		}
	}
	// Committed: dependency-exact invalidation for every OTHER subscribed session (§6),
	// AND event wakes for workflows parked on this resource via taak.onChange
	// (BUILD-D D4, ADR-05 §5): both ride the SAME mutation transaction, so a derived
	// write reaches live sessions and parked workflows atomically with the commit.
	if err := notifyInvalidate(ctx, conn, vm.Resource, sess.RowID, sess.Horizon); err != nil {
		return err
	}
	if _, err := cfr.WakeEvents(ctx, conn, vm.Resource, sess.RowID); err != nil {
		return err
	}
	sess.UILocal["__alert__"] = ""
	sess.Draft = map[string]string{}
	sess.RowVersion = strconv.FormatInt(base+1, 10) // this form now owns the new version
	return nil
}

// notifyInvalidate publishes NOTIFY (resource, rowId, horizon) on the reactive
// invalidation channel (ADR-11 §6). Fires on commit.
func notifyInvalidate(ctx context.Context, conn cfr.DB, resource, rowID, horizon string) error {
	payload := resource + "\x1f" + rowID + "\x1f" + horizon
	_, err := conn.Exec(ctx, `SELECT pg_notify('regel_invalidate', $1)`, payload)
	return err
}

// --- validation ---------------------------------------------------------------

// validateField is the D3 server-authoritative field validator (a minimal, honest
// subset of the D1 R.parse bundle): type-shape checks by base. Empty is permitted
// (optionality is a named residue). Returns "" on success or a reason string.
func validateField(f kfield, val string) string {
	if val == "" {
		return ""
	}
	switch f.Base {
	case "number", "money":
		if _, err := strconv.ParseFloat(val, 64); err != nil {
			return "must be a number"
		}
	case "boolean":
		if val != "true" && val != "false" {
			return "must be true or false"
		}
	case "email":
		if !strings.Contains(val, "@") {
			return "must be an email address"
		}
	case "url":
		if !strings.Contains(val, "://") {
			return "must be a URL"
		}
	}
	return ""
}

// --- size cap (ADR-11 §5) -----------------------------------------------------

// sessionCapBytes is the per-session CFR byte cap. Breach truncates to mount
// expression + form draft + subscriptions (draft preserved); still over ⇒ the
// session closes with an alert frame.
const sessionCapBytes = 256 * 1024

// checkSizeCap enforces the §5 256KB cap at checkpoint. On breach it truncates the
// session's UI-local + last-sent-snapshot bulk, preserving the mount expression and
// the form draft; if still over it marks the session closing (an alert frame is the
// caller's job on the next render — here we drop the snapshot to force reconvergence).
func (s *Server) checkSizeCap(sess *sessionCFR) error {
	frames, err := sess.frames()
	if err != nil {
		return err
	}
	if len(frames) <= sessionCapBytes {
		return nil
	}
	// Truncate: keep mount expr + form draft + (subscriptions live in their own
	// table, not the CFR), drop the retained snapshot/UI-local bulk.
	sess.LastSnap = map[string]string{}
	sess.RowKeys = nil
	sess.UILocal = map[string]string{"__alert__": "session state truncated (size cap)"}
	sess.setDigest(0)
	return nil
}

// --- helpers ------------------------------------------------------------------

func matFromSnap(snap map[string]string) map[string]ui.Materialized {
	out := make(map[string]ui.Materialized, len(snap))
	for id, v := range snap {
		out[id] = ui.Materialized{Snapshot: v}
	}
	return out
}

func listRowsFromKeys(keys []string) []ui.ListRow {
	out := make([]ui.ListRow, len(keys))
	for i, k := range keys {
		out[i] = ui.ListRow{Key: k}
	}
	return out
}

func findField(vm *viewMeta, name string) *kfield {
	for i := range vm.Fields {
		if vm.Fields[i].Name == name {
			return &vm.Fields[i]
		}
	}
	return nil
}

// fieldForSlot maps a template slot id to its backing field name.
func fieldForSlot(vm *viewMeta, kind, slotID string) string {
	t := vm.template(kind)
	if t == nil {
		return ""
	}
	base := slotID
	if i := strings.IndexByte(slotID, '#'); i >= 0 {
		base = slotID[:i]
	}
	for _, sl := range t.Slots {
		if sl.ID == base {
			return sl.Field
		}
	}
	return ""
}

func draftFieldsSorted(sess *sessionCFR) []string {
	out := make([]string, 0, len(sess.Draft))
	for k := range sess.Draft {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprintf
var _ = pgwire.CodeSerializationFailure
