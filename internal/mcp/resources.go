package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

// resources.go serves the ADR-12 §2 six read-only resources. Each passes the SAME
// scope filter as the tools; catalog://verdict/{id} is caller-scoped exactly like
// verdict.get, and every name-addressed miss shares the §3 floored NOT_FOUND path.

func resourceSpecs() []map[string]any {
	res := func(uri, name, desc string) map[string]any {
		return map[string]any{"uri": uri, "name": name, "description": desc, "mimeType": "application/json"}
	}
	return []map[string]any{
		res("catalog://definition/{hash}", "definition", "A definition by content hash (scope-filtered)."),
		res("catalog://name/{qname}", "name", "A definition by canonical qname (name@scope)."),
		res("catalog://resource/{name}/schema", "resource-schema", "A derived resource's schema + OpenAPI."),
		res("catalog://epoch", "epoch", "Epoch, dialect version, and std module roster."),
		res("catalog://verifier-coverage", "verifier-coverage", "ADR-07 §5 verifier coverage rows."),
		res("catalog://verdict/{id}", "verdict", "A Verdict by patch_id|refusal_id (own-principal only)."),
	}
}

// handleResourceRead resolves a catalog:// URI to its JSON content.
func (s *Server) handleResourceRead(ctx context.Context, sess *Session, req rpcRequest) rpcResponse {
	var a struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &a); err != nil {
		return errResp(req.ID, codeInvalidParams, err.Error())
	}
	return s.withConn(ctx, sess, req.ID, func(conn *pgwire.Conn, p admission.Principal) rpcResponse {
		body, ok, rerr := s.readResource(ctx, conn, p, a.URI)
		if rerr != nil {
			return rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: rerr}
		}
		if !ok {
			body = notFoundResult()
		}
		text, _ := json.Marshal(body)
		return okResp(req.ID, map[string]any{
			"contents": []map[string]any{{"uri": a.URI, "mimeType": "application/json", "text": string(text)}},
		})
	})
}

func (s *Server) readResource(ctx context.Context, conn *pgwire.Conn, p admission.Principal, uri string) (any, bool, *rpcError) {
	rest, ok := strings.CutPrefix(uri, "catalog://")
	if !ok {
		return nil, false, &rpcError{Code: codeInvalidParams, Message: "unknown resource scheme: " + uri}
	}
	start := time.Now()
	switch {
	case strings.HasPrefix(rest, "definition/"):
		hash := strings.TrimPrefix(rest, "definition/")
		res, found, err := catalogGetByHash(ctx, conn, p.Chain, hash)
		if err != nil {
			return nil, false, internalErr(err)
		}
		if !found {
			floorNotFound(start)
			return nil, false, nil
		}
		return res, true, nil

	case strings.HasPrefix(rest, "name/"):
		qname := strings.TrimPrefix(rest, "name/")
		res, found, err := catalogGetByQName(ctx, conn, p.Chain, qname)
		if err != nil {
			return nil, false, internalErr(err)
		}
		if !found {
			floorNotFound(start)
			return nil, false, nil
		}
		return res, true, nil

	case strings.HasPrefix(rest, "resource/") && strings.HasSuffix(rest, "/schema"):
		name := strings.TrimSuffix(strings.TrimPrefix(rest, "resource/"), "/schema")
		di, found, err := resolveDerived(ctx, conn, p.Chain, name)
		if err != nil {
			return nil, false, internalErr(err)
		}
		if !found {
			floorNotFound(start)
			return nil, false, nil
		}
		return resourceSchema(name, di), true, nil

	case rest == "epoch":
		return s.epochResource(ctx, conn), true, nil

	case rest == "verifier-coverage":
		rows, err := s.coverageResource(ctx, conn)
		if err != nil {
			return nil, false, internalErr(err)
		}
		return map[string]any{"epoch": s.epoch, "coverage": rows}, true, nil

	case strings.HasPrefix(rest, "verdict/"):
		id := strings.TrimPrefix(rest, "verdict/")
		v, found, err := admission.FetchVerdict(ctx, conn, id, p.Subject())
		if err != nil {
			return nil, false, internalErr(err)
		}
		if !found {
			floorNotFound(start)
			return nil, false, nil
		}
		return v, true, nil
	}
	return nil, false, &rpcError{Code: codeInvalidParams, Message: "unknown resource: " + uri}
}

// resourceSchema renders a derived resource's schema + a minimal OpenAPI shape
// (PII fields flagged; masking is the read rule, ADR-12 §4).
func resourceSchema(name string, di *derivedInfo) map[string]any {
	props := map[string]any{}
	for fn, f := range di.Fields {
		t := "string"
		switch f.Base {
		case "number":
			t = "number"
		case "boolean":
			t = "boolean"
		}
		props[fn] = map[string]any{"type": t, "pii": f.PII, "base": f.Base}
	}
	return map[string]any{
		"resource":   makeQName(name, di.ScopeKind, di.ScopeID),
		"table":      di.TableName,
		"jsonSchema": map[string]any{"type": "object", "properties": props},
		"openapi": map[string]any{
			"paths": map[string]any{
				"/" + di.TableName: map[string]any{"get": map[string]any{"summary": "query " + name}},
			},
		},
	}
}

func (s *Server) epochResource(ctx context.Context, conn *pgwire.Conn) map[string]any {
	var root, attest string
	_, _ = conn.QueryRow(ctx,
		`SELECT std_manifest_root, dispatch_attestation FROM epoch WHERE n=$1`,
		[]any{s.epoch}, &root, &attest)
	var stdRoster []string
	rows, err := conn.Query(ctx,
		`SELECT name FROM name_pointer WHERE scope_kind=0 AND scope_id='' AND name LIKE 'std/%' ORDER BY name`)
	if err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				stdRoster = append(stdRoster, n)
			}
		}
	}
	return map[string]any{
		"epoch":             s.epoch,
		"dialect_version":   "ts7-strict",
		"std_manifest_root": root,
		"std_modules":       stdRoster,
	}
}

func (s *Server) coverageResource(ctx context.Context, conn *pgwire.Conn) ([]map[string]any, error) {
	rows, err := conn.Query(ctx,
		`SELECT component, threat_class_ids, corpus_case_count, mutation_score
		 FROM verifier_coverage WHERE epoch=$1 ORDER BY component`, s.epoch)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var comp string
		var threats []string
		var corpus int
		var score float64
		if err := rows.Scan(&comp, &threats, &corpus, &score); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, map[string]any{
			"component": comp, "threat_classes": threats, "corpus_cases": corpus, "mutation_score": score,
		})
	}
	return out, rows.Err()
}
