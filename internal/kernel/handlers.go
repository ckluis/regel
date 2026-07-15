package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// --- POST /admit -------------------------------------------------------------

func (s *Server) handleAdmit(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	var patch admission.Patch
	if err := json.Unmarshal(body, &patch); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad patch json: " + err.Error()})
		return
	}
	auth := authFromHeader(r)

	conn, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer s.pool.Release(conn)

	v, err := admission.Admit(r.Context(), conn, patch, auth, s.image)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	// ADR-09 post-admission hook: on a green admission, advance the git projection
	// mirror (a pure fold + self-heal). Off the eval path; a projection error never
	// fails the admission — the CLI `regel project` and self-heal reconcile later.
	if s.mirror != nil && (v.Outcome == admission.OutcomeAdmitted || v.Outcome == admission.OutcomeAlreadyAdmitted) {
		_, _ = s.mirror.Advance(r.Context(), conn)
	}
	writeJSON(w, admission.HTTPStatus(v.Outcome), v)
}

// authFromHeader binds the authenticated principal. STAGE-A dev stub: the
// X-Regel-Actor header carries "kind:id" (e.g. "engineer:dev"); a real deploy
// authenticates the caller. The patch body never sets identity (§2a).
func authFromHeader(r *http.Request) admission.Principal {
	kind, id := "engineer", "anonymous"
	if h := r.Header.Get("X-Regel-Actor"); h != "" {
		if i := strings.IndexByte(h, ':'); i >= 0 {
			kind, id = h[:i], h[i+1:]
		} else {
			id = h
		}
	}
	return admission.Principal{ActorKind: kind, ActorID: id, Via: "mcp"}
}

// --- POST /eval/{name...} ----------------------------------------------------

func (s *Server) handleEval(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	args, err := parseArgs(body)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}

	req := EvalRequest{
		Name:  name,
		Args:  args,
		Chain: chainFromHeader(r),
		Tier:  cek.TierTrusted,
	}
	if t := r.URL.Query().Get("tier"); t == "sandbox" {
		req.Tier = cek.TierSandbox
		if f := r.URL.Query().Get("fuel"); f != "" {
			if n, e := strconv.ParseInt(f, 10, 64); e == nil {
				req.Fuel = n
			}
		}
	}
	if a := r.URL.Query().Get("as_of"); a != "" {
		t, e := time.Parse(time.RFC3339, a)
		if e != nil {
			writeJSON(w, 400, map[string]string{"error": "bad as_of: " + e.Error()})
			return
		}
		req.AsOf = &t
	}

	res, err := s.Eval(r.Context(), req)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if res.Transitions > 0 {
		w.Header().Set("X-Regel-Transitions", strconv.FormatInt(res.Transitions, 10))
	}
	writeJSON(w, res.Status, res.Body)
}

// EvalRequest is a resolved evaluation request.
type EvalRequest struct {
	Name  string
	Args  []cek.Value
	Chain catalog.Chain
	AsOf  *time.Time
	Tier  cek.Tier
	Fuel  int64
}

// EvalResult is what an evaluation produced, ready to serialize.
type EvalResult struct {
	Status      int
	Body        any
	Transitions int64
}

// Eval resolves a name, runs it under the requested tier/budget, and on a park
// writes a durable continuation (ADR-05) and returns 202. Shared by the HTTP
// door and the CLI eval helper.
func (s *Server) Eval(ctx context.Context, req EvalRequest) (EvalResult, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return EvalResult{}, err
	}
	resolved, ok, rerr := catalog.Resolve(ctx, conn, catalog.ResolveReq{
		Name: req.Name, Chain: req.Chain, CallerModule: "", AsOf: req.AsOf,
	})
	s.pool.Release(conn)
	if rerr != nil {
		return EvalResult{}, rerr
	}
	if !ok {
		return EvalResult{Status: 404, Body: map[string]string{"error": "name does not resolve: " + req.Name}}, nil
	}

	out := s.interp.Run(ctx, cek.RunReq{
		DefHash:   resolved.Hash,
		Args:      req.Args,
		Tier:      req.Tier,
		Fuel:      req.Fuel,
		Principal: cek.Principal{Subject: "eval", IsOperator: true},
	})

	switch out.Kind {
	case cek.OutDone:
		return EvalResult{Status: 200, Body: valueToJSON(out.Value), Transitions: out.Transitions}, nil
	case cek.OutParked:
		return s.park(ctx, resolved.Hash, out)
	case cek.OutFaulted:
		return EvalResult{Status: 500, Body: map[string]any{"fault": valueToJSON(out.Fault)}, Transitions: out.Transitions}, nil
	default:
		msg := "internal evaluation error"
		if out.Err != nil {
			msg = out.Err.Error()
		}
		return EvalResult{Status: 500, Body: map[string]any{"error": msg}}, nil
	}
}

func (s *Server) park(ctx context.Context, rootHash string, out cek.Outcome) (EvalResult, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return EvalResult{}, err
	}
	defer s.pool.Release(conn)
	contID, condID, perr := cfr.Park(ctx, conn, cfr.ParkReq{
		State:       out.State,
		Kind:        "request",
		RootDefHash: rootHash,
		Class:       out.Condition.Class,
		Payload:     out.Condition.Payload,
		Restarts:    out.Condition.Restarts,
		Principal:   map[string]any{"subject": "eval"},
	})
	if perr != nil {
		return EvalResult{}, perr
	}
	return EvalResult{Status: 202, Body: map[string]any{
		"continuation_id": contID,
		"condition_id":    condID,
		"class":           out.Condition.Class,
		"restarts":        restartsJSON(out.Condition.Restarts),
	}, Transitions: out.Transitions}, nil
}

// --- POST /workflow/{name...} ------------------------------------------------

// handleStartWorkflow resolves a name and starts a durable workflow continuation
// driven by the reactor (ADR-06 §4). Replies 202 with the continuation id.
func (s *Server) handleStartWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.Draining() {
		writeJSON(w, 503, map[string]string{"error": "kernel draining (epoch fence)"})
		return
	}
	name := r.PathValue("name")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	args, err := parseArgs(body)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	conn, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	resolved, ok, rerr := catalog.Resolve(r.Context(), conn, catalog.ResolveReq{
		Name: name, Chain: chainFromHeader(r), CallerModule: "",
	})
	if rerr != nil {
		s.pool.Release(conn)
		writeJSON(w, 500, map[string]string{"error": rerr.Error()})
		return
	}
	if !ok {
		s.pool.Release(conn)
		writeJSON(w, 404, map[string]string{"error": "name does not resolve: " + name})
		return
	}
	actor := authFromHeader(r)
	principal := map[string]any{"subject": actor.ActorID, "operator": actor.ActorKind == "operator"}
	contID, serr := cfr.StartWorkflow(r.Context(), conn, s.stepEnv(0), s.interp,
		resolved.Hash, args, principal, cek.TierTrusted)
	s.pool.Release(conn)
	if serr != nil {
		writeJSON(w, 500, map[string]string{"error": serr.Error()})
		return
	}
	writeJSON(w, 202, map[string]any{"continuation_id": contID})
}

// --- POST /channel/{channel}/send --------------------------------------------

// handleChannelSend lands a channel message and wakes the oldest matching sleeping
// receiver (ADR-05 §5 BUILD-B external send). Replies 200 {delivered_to}.
func (s *Server) handleChannelSend(w http.ResponseWriter, r *http.Request) {
	if s.Draining() {
		writeJSON(w, 503, map[string]string{"error": "kernel draining (epoch fence)"})
		return
	}
	channel := r.PathValue("channel")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad send json: " + err.Error()})
		return
	}
	conn, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer s.pool.Release(conn)
	actor := authFromHeader(r)
	to, serr := cfr.SendChannel(r.Context(), conn, s.stepEnv(0), channel, jsonToValue(req.Value), actor.ActorID)
	if serr != nil {
		writeJSON(w, 500, map[string]string{"error": serr.Error()})
		return
	}
	var delivered any
	if to != "" {
		delivered = to
	}
	writeJSON(w, 200, map[string]any{"delivered_to": delivered})
}

// --- GET /healthz ------------------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"kernel_id": s.kernelID,
		"epoch":     s.epoch,
		"draining":  s.Draining(),
		"metrics":   cfr.MetricsSnapshot(),
		"sse":       sseMetricsSnapshot(),
	})
}

// --- GET /continuation/{id} --------------------------------------------------

func (s *Server) handleContinuation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conn, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer s.pool.Release(conn)

	var status string
	found, err := conn.QueryRow(r.Context(),
		`SELECT status FROM continuation WHERE id = $1`, []any{id}, &status)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "no such continuation"})
		return
	}
	resp := map[string]any{"continuation_id": id, "status": status}
	if status == "done" {
		if v, ok, lerr := cfr.LoadResult(r.Context(), conn, id); lerr == nil && ok {
			resp["result"] = valueToJSON(v)
		}
	}

	var condID, class, cstatus string
	cfound, err := conn.QueryRow(r.Context(), `
SELECT id, class, status FROM durable_condition
WHERE continuation_id = $1 ORDER BY signaled_at DESC LIMIT 1`,
		[]any{id}, &condID, &class, &cstatus)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if cfound {
		resp["condition"] = map[string]any{"id": condID, "class": class, "status": cstatus}
		restarts, rerr := loadRestarts(r.Context(), conn, condID)
		if rerr != nil {
			writeJSON(w, 500, map[string]string{"error": rerr.Error()})
			return
		}
		resp["restarts"] = restarts
	}
	writeJSON(w, 200, resp)
}

func loadRestarts(ctx context.Context, conn *pgwire.Conn, condID string) ([]map[string]any, error) {
	rows, err := conn.Query(ctx,
		`SELECT name, label, COALESCE(capability_required, '') FROM restart WHERE condition_id = $1 ORDER BY name`,
		condID)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		var name, label, cap string
		if err := rows.Scan(&name, &label, &cap); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, map[string]any{"name": name, "label": label, "capability_required": cap})
	}
	return out, rows.Err()
}

// --- POST /continuation/{id}/restart -----------------------------------------

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	contID := r.PathValue("id")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		Restart string         `json:"restart"`
		Args    map[string]any `json:"args"`
		// ExpectedHash is the SHA-256 of the condition's continuation frames blob at
		// render time (ADR-12 §7). Optional here for back-compat; when present the
		// shared fence rejects a moved continuation with CONDITION_MOVED.
		ExpectedHash string `json:"expected_hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad restart json: " + err.Error()})
		return
	}
	if req.Restart == "" {
		writeJSON(w, 400, map[string]string{"error": "missing restart name"})
		return
	}

	conn, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer s.pool.Release(conn)

	// Find the open condition for this continuation.
	var condID string
	found, err := conn.QueryRow(r.Context(), `
SELECT id FROM durable_condition
WHERE continuation_id = $1 AND status = 'open' ORDER BY signaled_at DESC LIMIT 1`,
		[]any{contID}, &condID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "no open condition for continuation"})
		return
	}

	resume := func(state *cek.State, choice cek.RestartChoice) cek.Outcome {
		// STAGE-A dev stub: the resume principal is the operator (as the eval path).
		return s.interp.Resume(context.Background(), state, cek.Delivery{Restart: &choice},
			cek.Principal{Subject: "operator", IsOperator: true})
	}
	// The shared fenced path (ADR-12 §7): one implementation, two doors — the same
	// cfr.ResolveConditionFenced the MCP condition.restart tool calls.
	out, rerr := cfr.ResolveConditionFenced(r.Context(), conn, condID, req.Restart, req.ExpectedHash,
		req.Args, "operator", []string{"operator"}, resume)
	if rerr != nil {
		writeJSON(w, restartErrStatus(rerr), map[string]string{"error": rerr.Error()})
		return
	}

	switch out.Kind {
	case cek.OutDone:
		writeJSON(w, 200, valueToJSON(out.Value))
	case cek.OutParked:
		writeJSON(w, 202, map[string]any{"class": out.Condition.Class, "restarts": restartsJSON(out.Condition.Restarts)})
	case cek.OutFaulted:
		writeJSON(w, 500, map[string]any{"fault": valueToJSON(out.Fault)})
	default:
		msg := "internal error on resume"
		if out.Err != nil {
			msg = out.Err.Error()
		}
		writeJSON(w, 500, map[string]any{"error": msg})
	}
}

// --- helpers -----------------------------------------------------------------

// restartErrStatus maps a fenced-restart error to its HTTP status (ADR-12 §7).
func restartErrStatus(err error) int {
	switch {
	case errors.Is(err, cfr.ErrRestartNotFound):
		return 404
	case errors.Is(err, cfr.ErrConditionMoved), errors.Is(err, cfr.ErrConditionResolved):
		return 409
	case errors.Is(err, cfr.ErrCapabilityRefused):
		return 403
	default:
		return 500
	}
}

func chainFromHeader(r *http.Request) catalog.Chain {
	var c catalog.Chain
	// "org:X" / "team:Y" / "user:Z" / "package:P" comma-separated (dev stub).
	for _, part := range strings.Split(r.Header.Get("X-Regel-Scope"), ",") {
		part = strings.TrimSpace(part)
		i := strings.IndexByte(part, ':')
		if i < 0 {
			continue
		}
		switch part[:i] {
		case "user":
			c.UserID = part[i+1:]
		case "team":
			c.TeamID = part[i+1:]
		case "org":
			c.OrgID = part[i+1:]
		case "package":
			c.PackageID = part[i+1:]
		}
	}
	return c
}

func parseArgs(body []byte) ([]cek.Value, error) {
	if len(body) == 0 || strings.TrimSpace(string(body)) == "" {
		return nil, nil
	}
	var raw []any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("args must be a JSON array: %w", err)
	}
	out := make([]cek.Value, len(raw))
	for i, a := range raw {
		out[i] = jsonToValue(a)
	}
	return out, nil
}

func restartsJSON(rs []cek.Restart) []map[string]any {
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]any{"name": r.Name, "label": r.Label, "capability_required": r.CapabilityRequired})
	}
	return out
}
