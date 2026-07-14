package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

// prompts.go serves the ADR-12 §2 three prompts (user-triggered scaffolds):
// author-resource, author-workflow, and fix-verdict (feeds a Verdict's diagnostics
// + fix fields back as an editing brief). fix-verdict is caller-scoped through
// admission.FetchVerdict, so it can only brief on the caller's own verdicts.

func promptSpecs() []map[string]any {
	arg := func(name, desc string, required bool) map[string]any {
		return map[string]any{"name": name, "description": desc, "required": required}
	}
	return []map[string]any{
		{"name": "author-resource", "description": "Scaffold a strict-TS resource(...) declaration for admission.",
			"arguments": []map[string]any{arg("name", "resource name", false), arg("fields", "field sketch", false)}},
		{"name": "author-workflow", "description": "Scaffold a strict-TS durable workflow for admission.",
			"arguments": []map[string]any{arg("name", "workflow name", false), arg("goal", "what it should do", false)}},
		{"name": "fix-verdict", "description": "Turn a Verdict's diagnostics + fixes into an editing brief.",
			"arguments": []map[string]any{arg("id", "patch_id or refusal_id", true)}},
	}
}

func (s *Server) handlePromptGet(ctx context.Context, sess *Session, req rpcRequest) rpcResponse {
	var a struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &a); err != nil {
		return errResp(req.ID, codeInvalidParams, err.Error())
	}
	return s.withConn(ctx, sess, req.ID, func(conn *pgwire.Conn, p admission.Principal) rpcResponse {
		msg, desc, rerr := s.buildPrompt(ctx, conn, p, a.Name, a.Arguments)
		if rerr != nil {
			return rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: rerr}
		}
		return okResp(req.ID, map[string]any{
			"description": desc,
			"messages": []map[string]any{
				{"role": "user", "content": map[string]any{"type": "text", "text": msg}},
			},
		})
	})
}

func (s *Server) buildPrompt(ctx context.Context, conn *pgwire.Conn, p admission.Principal, name string, args map[string]string) (string, string, *rpcError) {
	switch name {
	case "author-resource":
		rn := orDefault(args["name"], "Deal")
		return fmt.Sprintf(`Author a regel resource named %q for admission through patch.submit.
Dialect: closed-world strict TypeScript 7 (no class/new/generators/C-style for). Use:

  import { resource } from "std/resource";
  export const %s = resource({
    fields: { title: "text" /* %s */ },
  });

PII fields use "pii:text" | "pii:email" | "pii:phone" (maskable). Submit commit:false first,
read the Verdict's diagnostics[].fix, iterate, then commit:true.`, rn, rn, orDefault(args["fields"], "add fields")),
			"author-resource scaffold", nil

	case "author-workflow":
		wn := orDefault(args["name"], "onboard")
		return fmt.Sprintf(`Author a regel durable workflow named %q for admission.
Do not hold a std/sql Conn across an await (V5 capture). Sketch:

  import { sleep } from "std/wf";
  export async function %s(): Promise<void> {
    await sleep(1);
    // %s
  }

Submit commit:false, read diagnostics[].fix, iterate, then commit:true.`, wn, wn, orDefault(args["goal"], "the steps")),
			"author-workflow scaffold", nil

	case "fix-verdict":
		id := args["id"]
		v, ok, err := admission.FetchVerdict(ctx, conn, id, p.Subject())
		if err != nil {
			return "", "", internalErr(err)
		}
		if !ok {
			return "No verdict found for that id (or it is not yours). Re-check the id.", "fix-verdict (not found)", nil
		}
		return fixVerdictBrief(id, v), "fix-verdict editing brief", nil
	}
	return "", "", &rpcError{Code: codeInvalidParams, Message: "unknown prompt: " + name}
}

func fixVerdictBrief(id string, v admission.Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Editing brief for verdict %s (outcome=%s).\n", id, v.Outcome)
	if len(v.Diagnostics) == 0 {
		b.WriteString("No diagnostics — this patch is admissible as-is.\n")
		return b.String()
	}
	b.WriteString("Fix each diagnostic below, then re-submit commit:false to converge:\n\n")
	for i, d := range v.Diagnostics {
		fmt.Fprintf(&b, "%d. [%s/%s] %s\n", i+1, d.StageOrVerifier, d.Code, d.Message)
		if d.Fix != "" {
			fmt.Fprintf(&b, "   FIX: %s\n", d.Fix)
		}
		if d.Loc.Span != "" {
			fmt.Fprintf(&b, "   AT: %s\n", d.Loc.Span)
		}
	}
	return b.String()
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
