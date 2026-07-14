package mcp

import (
	"context"
	"encoding/json"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// tools.go implements the ADR-12 §2 eleven-tool roster. Every handler calls the
// SAME internal functions the HTTP/CLI doors use (one gate, N doors): patch.submit
// is admission.Admit/DryRun, verdict.get is admission.FetchVerdict, condition.restart
// is cfr.ResolveConditionFenced. No tool is stubbed to a fake verdict.

// toolSpec is one tools/list entry.
func toolSpecs() []map[string]any {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	str := map[string]any{"type": "string"}
	return []map[string]any{
		{"name": "catalog.search", "description": "Search scope-visible definitions; returns qname per result (no source, no data).",
			"inputSchema": obj(map[string]any{"query": str, "kind": str, "scope": str})},
		{"name": "catalog.get", "description": "Fetch a definition by hash or qname (code, never data).",
			"inputSchema": obj(map[string]any{"hash": str, "qname": str, "asOf": str})},
		{"name": "catalog.deps", "description": "Dependency edges (dir=out) or dependents (dir=in) of a hash.",
			"inputSchema": obj(map[string]any{"hash": str, "dir": str})},
		{"name": "resource.query", "description": "Query rows from a derived resource, PII masked always.",
			"inputSchema": obj(map[string]any{"resource": str, "filter": map[string]any{"type": "object"}, "limit": map[string]any{"type": "integer"}})},
		{"name": "resource.mutate", "description": "Insert/update a derived-resource row under policy + row-version guard.",
			"inputSchema": obj(map[string]any{"resource": str, "op": str, "id": map[string]any{"type": "integer"}, "values": map[string]any{"type": "object"}, "baseVersion": map[string]any{"type": "integer"}})},
		{"name": "patch.submit", "description": "Submit a patch. commit:false is a dry-run (full pipeline, rolled back); commit:true is the real gate.",
			"inputSchema": obj(map[string]any{"source": str, "scope": str, "message": str, "module": str, "commit": map[string]any{"type": "boolean"}, "approvalToken": str, "declare": map[string]any{"type": "array"}, "readLog": map[string]any{"type": "array"}})},
		{"name": "verdict.get", "description": "Fetch a Verdict by patch_id or refusal_id (own-principal only).",
			"inputSchema": obj(map[string]any{"id": str})},
		{"name": "workflow.inspect", "description": "Inspect a continuation's status/wake/conditions (payloads masked).",
			"inputSchema": obj(map[string]any{"continuation_id": str})},
		{"name": "condition.list", "description": "List open durable conditions with restarts + expectedHash.",
			"inputSchema": obj(map[string]any{"scope": str, "status": str})},
		{"name": "condition.restart", "description": "Resolve a durable condition by a restart (DISABLED for agent principals at Stage C).",
			"inputSchema": obj(map[string]any{"condition_id": str, "restart_name": str, "expectedHash": str, "args": map[string]any{"type": "object"}})},
		{"name": "audit.query", "description": "Admission/mutation audit rows for a subject (masked, scope-filtered).",
			"inputSchema": obj(map[string]any{"subject": str, "since": str})},
	}
}

// handleToolCall parses {name, arguments} and dispatches to the tool.
func (s *Server) handleToolCall(ctx context.Context, sess *Session, req rpcRequest) rpcResponse {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		return errResp(req.ID, codeInvalidParams, err.Error())
	}
	return s.withConn(ctx, sess, req.ID, func(conn *pgwire.Conn, p admission.Principal) rpcResponse {
		result, rerr := s.callTool(ctx, conn, p, call.Name, call.Arguments)
		if rerr != nil {
			return rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: rerr}
		}
		return okResp(req.ID, wrapToolResult(result))
	})
}

// wrapToolResult renders a tool's structured output as an MCP tools/call result.
func wrapToolResult(v any) map[string]any {
	text, _ := json.Marshal(v)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
}

func (s *Server) callTool(ctx context.Context, conn *pgwire.Conn, p admission.Principal, name string, raw json.RawMessage) (any, *rpcError) {
	arg := func(v any) *rpcError {
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
		return nil
	}
	switch name {
	case "catalog.search":
		var a struct{ Query, Kind, Scope string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		rows, err := catalogSearch(ctx, conn, p.Chain, a.Query, a.Kind, a.Scope)
		if err != nil {
			return nil, internalErr(err)
		}
		if rows == nil {
			rows = []searchRow{}
		}
		return map[string]any{"results": rows}, nil

	case "catalog.get":
		var a struct{ Hash, QName, AsOf string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		start := time.Now()
		var res *getResult
		var ok bool
		var err error
		if a.Hash != "" {
			res, ok, err = catalogGetByHash(ctx, conn, p.Chain, a.Hash)
		} else {
			res, ok, err = catalogGetByQName(ctx, conn, p.Chain, a.QName)
		}
		if err != nil {
			return nil, internalErr(err)
		}
		if !ok {
			floorNotFound(start)
			return notFoundResult(), nil
		}
		return res, nil

	case "catalog.deps":
		var a struct{ Hash, Dir string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		if a.Dir == "" {
			a.Dir = "out"
		}
		start := time.Now()
		deps, ok, err := catalogDeps(ctx, conn, p.Chain, a.Hash, a.Dir)
		if err != nil {
			return nil, internalErr(err)
		}
		if !ok {
			floorNotFound(start)
			return notFoundResult(), nil
		}
		return map[string]any{"deps": deps}, nil

	case "resource.query":
		var a struct {
			Resource string
			Filter   map[string]any
			Limit    int
		}
		if e := arg(&a); e != nil {
			return nil, e
		}
		start := time.Now()
		res, ok, err := queryResource(ctx, conn, p.Chain, a.Resource, a.Filter, a.Limit)
		if err != nil {
			return nil, internalErr(err)
		}
		if !ok {
			floorNotFound(start)
			return notFoundResult(), nil
		}
		return res, nil

	case "resource.mutate":
		var a struct {
			Resource    string
			Op          string
			ID          int64
			Values      map[string]any
			BaseVersion int
		}
		if e := arg(&a); e != nil {
			return nil, e
		}
		res, err := mutateResource(ctx, conn, p, a.Resource, a.Op, a.ID, a.Values, a.BaseVersion)
		if err != nil {
			return nil, internalErr(err)
		}
		return res, nil

	case "patch.submit":
		return s.toolPatchSubmit(ctx, conn, p, raw)

	case "verdict.get":
		var a struct{ ID string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		start := time.Now()
		v, ok, err := admission.FetchVerdict(ctx, conn, a.ID, p.Subject())
		if err != nil {
			return nil, internalErr(err)
		}
		if !ok {
			floorNotFound(start)
			return notFoundResult(), nil
		}
		return v, nil

	case "workflow.inspect":
		var a struct {
			ContinuationID string `json:"continuation_id"`
		}
		if e := arg(&a); e != nil {
			return nil, e
		}
		start := time.Now()
		res, ok, err := inspectWorkflow(ctx, conn, p, a.ContinuationID)
		if err != nil {
			return nil, internalErr(err)
		}
		if !ok {
			floorNotFound(start)
			return notFoundResult(), nil
		}
		return res, nil

	case "condition.list":
		var a struct{ Scope, Status string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		res, err := listConditions(ctx, conn, p, a.Status)
		if err != nil {
			return nil, internalErr(err)
		}
		return map[string]any{"conditions": res}, nil

	case "condition.restart":
		return s.toolConditionRestart(ctx, conn, p, raw)

	case "audit.query":
		var a struct{ Subject, Since string }
		if e := arg(&a); e != nil {
			return nil, e
		}
		res, err := auditQuery(ctx, conn, p, a.Subject, a.Since)
		if err != nil {
			return nil, internalErr(err)
		}
		return map[string]any{"rows": res}, nil
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + name}
}

func internalErr(err error) *rpcError {
	return &rpcError{Code: codeInternalError, Message: err.Error()}
}

// --- patch.submit ------------------------------------------------------------

// readLogArg is one wire-form content-seeder read-log entry (ADR-12 §2/§6 BUILD-C):
// the provenance of a catalog/resource/condition/audit row the authoring session read.
// Scope is a qname scope token ("product", "org.<id>", …); absent ⇒ product.
type readLogArg struct {
	SourceKind string `json:"source_kind"`
	SourceRef  string `json:"source_ref"`
	Scope      string `json:"scope"`
	SeededBy   string `json:"seeded_by"`
}

func (s *Server) toolPatchSubmit(ctx context.Context, conn *pgwire.Conn, p admission.Principal, raw json.RawMessage) (any, *rpcError) {
	var a struct {
		Source        string       `json:"source"`
		Scope         string       `json:"scope"`
		Message       string       `json:"message"`
		Module        string       `json:"module"`
		Commit        bool         `json:"commit"`
		ApprovalToken string       `json:"approvalToken"`
		Declare       []string     `json:"declare"`
		ReadLog       []readLogArg `json:"readLog"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if a.Module == "" {
		a.Module = "app/agent"
	}
	// Scope binds from the AUTHENTICATED principal (never the body): default to the
	// agent's own overlay; an explicit scope still passes through the gate's §6
	// policy, which refuses an out-of-reach scope or product without a token.
	target := admission.AgentOverlayScope(p)
	if a.Scope != "" {
		if k, id, ok := parseScopeToken(a.Scope); ok {
			target = admission.Scope{Kind: k, ID: id}
		}
	}
	patch := admission.Patch{
		Modules:       []admission.ModuleSrc{{ModuleName: a.Module, Source: a.Source}},
		TargetScope:   target,
		BaseHashes:    map[string]string{},
		ApprovalToken: a.ApprovalToken,
	}
	if len(a.Declare) > 0 {
		patch.DefaultDeclared = a.Declare
	}
	// ADR-12 §6 / §2 BUILD-C (C7): the authoring session DECLARES the rows it read
	// via readLog; the gate validates each entry against THIS principal's scope chain
	// (step 2a: an out-of-chain seeder is unrepresentable ⇒ rejected) and projects the
	// set into the Verdict `seeders` + the admission row — the content-seeder third
	// principal. Scope still binds from the authenticated principal, never this body.
	for _, e := range a.ReadLog {
		sk, sid := 0, ""
		if e.Scope != "" {
			if k, id, ok := parseScopeToken(e.Scope); ok {
				sk, sid = k, id
			}
		}
		patch.ReadLog = append(patch.ReadLog, admission.ReadLogEntry{
			SourceKind: e.SourceKind, SourceRef: e.SourceRef,
			Scope: admission.Scope{Kind: sk, ID: sid}, SeededBy: e.SeededBy,
		})
	}
	var v admission.Verdict
	var err error
	if a.Commit {
		v, err = admission.Admit(ctx, conn, patch, p, s.image)
	} else {
		v, err = admission.DryRun(ctx, conn, patch, p, s.image)
	}
	if err != nil {
		return nil, internalErr(err)
	}
	return v, nil
}

// --- condition.restart -------------------------------------------------------

func (s *Server) toolConditionRestart(ctx context.Context, conn *pgwire.Conn, p admission.Principal, raw json.RawMessage) (any, *rpcError) {
	var a struct {
		ConditionID  string         `json:"condition_id"`
		RestartName  string         `json:"restart_name"`
		ExpectedHash string         `json:"expectedHash"`
		Args         map[string]any `json:"args"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	// SHIPS DISABLED for agent principals (ADR-12 §7 BUILD-C): the restart-decision
	// eval that would enable it first exists at Stage E; absent metric ⇒ disabled.
	// A typed refusal naming the gate — human/operator principals keep the authority.
	if p.ActorKind == "agent" {
		return map[string]any{
			"status": "refused",
			"code":   "RESTART_DISABLED",
			"gate":   "ADR-12 §7 restart-decision eval (Stage E)",
			"detail": "agent-facing condition.restart ships disabled until the restart-decision accuracy metric is green; use condition.list / workflow.inspect. Human operators retain the operator-plane restart buttons.",
		}, nil
	}
	resume := func(state *cek.State, choice cek.RestartChoice) cek.Outcome {
		return s.interp.Resume(context.Background(), state, cek.Delivery{Restart: &choice},
			cek.Principal{Subject: p.Subject(), IsOperator: true})
	}
	out, err := cfr.ResolveConditionFenced(ctx, conn, a.ConditionID, a.RestartName, a.ExpectedHash,
		a.Args, p.Subject(), []string{"operator"}, resume)
	if err != nil {
		return map[string]any{"status": "refused", "code": restartErrCode(err), "detail": err.Error()}, nil
	}
	return map[string]any{"status": outcomeStatus(out)}, nil
}

func restartErrCode(err error) string {
	switch {
	case err == cfr.ErrConditionMoved:
		return "CONDITION_MOVED"
	case err == cfr.ErrConditionResolved:
		return "ALREADY_RESOLVED"
	case err == cfr.ErrRestartNotFound:
		return "NOT_FOUND"
	case err == cfr.ErrCapabilityRefused:
		return "CAP_REFUSED"
	default:
		return "INTERNAL"
	}
}

func outcomeStatus(out cek.Outcome) string {
	switch out.Kind {
	case cek.OutDone:
		return "done"
	case cek.OutParked:
		return "parked"
	case cek.OutFaulted:
		return "faulted"
	default:
		return "resumed"
	}
}

// --- workflow.inspect / condition.list / audit.query -------------------------

func inspectWorkflow(ctx context.Context, conn *pgwire.Conn, p admission.Principal, contID string) (map[string]any, bool, error) {
	var status, wakeKind, principalSubj string
	found, err := conn.QueryRow(ctx, `
SELECT status, COALESCE(wake->>'kind',''), COALESCE(principal->>'subject','')
FROM continuation WHERE id=$1`, []any{contID}, &status, &wakeKind, &principalSubj)
	if err != nil || !found {
		return nil, false, err
	}
	// Scope filter: an agent sees only its own continuations (payloads masked either
	// way); operators/humans may inspect any.
	if p.ActorKind == "agent" && principalSubj != p.Subject() {
		return nil, false, nil
	}
	conds := []map[string]any{}
	rows, err := conn.Query(ctx,
		`SELECT class, status FROM durable_condition WHERE continuation_id=$1 ORDER BY signaled_at`, contID)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var class, cstatus string
		if err := rows.Scan(&class, &cstatus); err != nil {
			rows.Close()
			return nil, false, err
		}
		conds = append(conds, map[string]any{"class": class, "status": cstatus})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return map[string]any{
		"continuation_id": contID,
		"status":          status,
		"wake":            map[string]any{"kind": wakeKind},
		"payload":         maskToken, // payloads masked (ADR-12 §2)
		"conditions":      conds,
	}, true, nil
}

func listConditions(ctx context.Context, conn *pgwire.Conn, p admission.Principal, status string) ([]map[string]any, error) {
	if status == "" {
		status = "open"
	}
	rows, err := conn.Query(ctx, `
SELECT dc.id, dc.class, dc.continuation_id, encode(sha256(c.frames),'hex'),
       COALESCE(c.principal->>'subject','')
FROM durable_condition dc JOIN continuation c ON c.id=dc.continuation_id
WHERE dc.status=$1 ORDER BY dc.signaled_at`, status)
	if err != nil {
		return nil, err
	}
	type condRow struct {
		id, class, contID, frameHash, subj string
	}
	var raw []condRow
	for rows.Next() {
		var cr condRow
		if err := rows.Scan(&cr.id, &cr.class, &cr.contID, &cr.frameHash, &cr.subj); err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, cr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, cr := range raw {
		if p.ActorKind == "agent" && cr.subj != p.Subject() {
			continue // scope filter
		}
		restarts, err := loadRestartsFor(ctx, conn, cr.id)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"condition_id": cr.id,
			"class":        cr.class,
			"message":      maskToken, // message masked (attacker-influenceable text is inert data)
			"expectedHash": cr.frameHash,
			"restarts":     restarts,
		})
	}
	return out, nil
}

func loadRestartsFor(ctx context.Context, conn *pgwire.Conn, condID string) ([]map[string]any, error) {
	rows, err := conn.Query(ctx,
		`SELECT name, label, params_schema::text FROM restart WHERE condition_id=$1 ORDER BY name`, condID)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var name, label, schema string
		if err := rows.Scan(&name, &label, &schema); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, map[string]any{"name": name, "label": label, "params_schema": json.RawMessage(schema)})
	}
	return out, rows.Err()
}

func auditQuery(ctx context.Context, conn *pgwire.Conn, p admission.Principal, subject, since string) ([]map[string]any, error) {
	// Scope filter: an agent may only query its OWN admissions; operators may query
	// the named subject. subject "kind:id".
	target := subject
	if p.ActorKind == "agent" {
		target = p.Subject()
	}
	args := []any{target}
	sqlText := `
SELECT id, actor_kind, actor_id, via, to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSZ'), submitted_hashes
FROM admission WHERE (actor_kind || ':' || actor_id) = $1`
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err == nil {
			sqlText += " AND created_at >= $2"
			args = append(args, since)
		}
	}
	sqlText += " ORDER BY id DESC LIMIT 200"
	rows, err := conn.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, actorID, via, at string
		var hashes []string
		if err := rows.Scan(&id, &kind, &actorID, &via, &at, &hashes); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, map[string]any{
			"admission_id": id, "actor": kind + ":" + actorID, "via": via,
			"at": at, "hashes": hashes,
		})
	}
	return out, rows.Err()
}
