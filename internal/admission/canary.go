package admission

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// canary.go — the ADR-02 §5 world-rehash canary (A7 residue): the standing proof
// that every stored definition still rehashes to the content address it claims,
// in TWO legs (ADR-02 R1-10):
//
//   - encoder leg  — replay each stored AST through normalize→encode→hash and
//     assert no stored address moves (catches an encoder/printer change, and a
//     TAMPERED ast row: a flipped byte no longer hashes to its primary key).
//   - pipeline leg — re-run the full parse→lower pipeline from each app row's
//     canonical_text and assert hash(normalize(lower(parse(text)))) == the stored
//     address (the LOAD-BEARING leg: catches a silent parse/lower drift at the
//     text↔AST seam §1 deliberately severs).
//
// A non-empty result is RED: the store.scrubber_tripped golden signal (ADR-13 §2
// #16) fires and the CLI door / test exits nonzero. The canary NEVER mutates —
// it is detection only; repair is the self-certifying byte-restore (ADR-02 §5.5).
//
// BUILD-F R8 (STAGE-E §9 residue discharge): the pipeline leg now re-lowers app
// definitions at EVERY scope — product AND every overlay scope (org/team/user/
// package heads). Before this, the pipeline leg filtered `scope_kind=0 AND
// scope_id=''`, so an overlay-scoped def (an agent/tenant patch that shadows the
// product def for its own sandbox scope) was covered by the encoder leg alone: a
// text↔AST seam drift on an overlay-only def — its stored AST hashing fine but its
// canonical_text no longer re-lowering to that address — passed SILENTLY (witnessed
// in migrate_test.go TestOverlayScopeCanaryReLower). The overlay leg re-lowers with
// the SAME resolver admission itself uses (lowerPatch): product-scope resolution
// with an external caller module — overlay-scope import resolution is a Stage-B
// residue, so admission lowers overlay defs at product scope today and the canary
// re-lowers them identically (the same seam). When Stage-B lands real per-scope
// import resolution, both change together.
//
// std natives carry KNativeBody nodes the ADR-01 grammar has no lowering production
// for (image.go), so they are structurally un-relowerable by construction and
// covered by the encoder leg alone (name LIKE 'std/%' is still excluded from the
// pipeline leg). Every definition, std or app, at every scope, gets the encoder
// leg — so a tampered AST anywhere still screams.

// CanaryFinding is one (address, leg) alarm payload (ADR-02 §5).
type CanaryFinding struct {
	Hash    string `json:"hash"`
	Name    string `json:"name,omitempty"`
	Scope   string `json:"scope,omitempty"` // "kind:id" of the pointer re-lowered ("0:" = product)
	Leg     string `json:"leg"`             // "encoder" | "pipeline"
	Message string `json:"message"`
}

// WorldRehashCanary runs both legs over the whole historical corpus and returns
// the findings (empty = green). im supplies the std L0 surface the pipeline leg's
// tsx path map needs (unused by the encoder leg).
func WorldRehashCanary(ctx context.Context, conn *pgwire.Conn, im *Image) ([]CanaryFinding, error) {
	var findings []CanaryFinding

	// --- encoder leg: every stored definition ---
	rows, err := conn.Query(ctx, `SELECT hash, encode(ast,'hex') FROM definition`)
	if err != nil {
		return nil, err
	}
	type defrow struct{ hash, astHex string }
	var defs []defrow
	for rows.Next() {
		var d defrow
		if err := rows.Scan(&d.hash, &d.astHex); err != nil {
			rows.Close()
			return nil, err
		}
		defs = append(defs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, d := range defs {
		if msg := checkDefEncoderLeg(d.hash, d.astHex); msg != "" {
			findings = append(findings, CanaryFinding{Hash: d.hash, Leg: "encoder", Message: msg})
		}
	}

	// --- pipeline leg: app definitions at EVERY scope (current heads) ---
	// BUILD-F R8: product AND overlay heads (scope_kind/scope_id carried), std
	// excluded (un-relowerable native bodies). A def pointed at from multiple
	// scopes is re-lowered once per pointer — an overlay-only def (unreachable
	// from product) is now inspected where before it was pipeline-leg-invisible.
	prows, err := conn.Query(ctx, `
SELECT p.name, p.scope_kind, p.scope_id, d.hash, d.canonical_text
  FROM name_pointer p JOIN definition d ON d.hash = p.hash
 WHERE p.name NOT LIKE 'std/%'
 ORDER BY p.scope_kind, p.scope_id, p.name`)
	if err != nil {
		return nil, err
	}
	type approw struct {
		name, hash, text string
		scopeKind        int
		scopeID          string
	}
	var apps []approw
	for prows.Next() {
		var a approw
		if err := prows.Scan(&a.name, &a.scopeKind, &a.scopeID, &a.hash, &a.text); err != nil {
			prows.Close()
			return nil, err
		}
		apps = append(apps, a)
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	for _, a := range apps {
		if msg := checkDefPipelineLeg(ctx, conn, a.name, a.hash, a.text); msg != "" {
			findings = append(findings, CanaryFinding{
				Hash: a.hash, Name: a.name, Scope: scopeKey(Scope{Kind: a.scopeKind, ID: a.scopeID}),
				Leg: "pipeline", Message: msg,
			})
		}
	}

	if len(findings) > 0 {
		emitScrubberTripped(findings)
	}
	return findings, nil
}

// checkDefEncoderLeg replays one stored AST through decode→normalize→hash and
// returns "" if the address holds, else the mismatch message (the ADR-02 §5
// encoder-leg red). A row whose bytes were tampered either fails to decode or
// hashes to a DIFFERENT address than its own primary key.
func checkDefEncoderLeg(hash, astHex string) string {
	ast, err := hex.DecodeString(astHex)
	if err != nil {
		return "ast column is not decodable hex: " + err.Error()
	}
	node, derr := rast.Decode(ast)
	if derr != nil {
		return "stored AST does not decode: " + derr.Error()
	}
	_, addr := rast.NormalizeAndAddress(node)
	if addr != hash {
		return fmt.Sprintf("stored AST rehashes to %s, not its address %s (tamper/encoder drift)", addr[:16], hash[:16])
	}
	return ""
}

// checkDefPipelineLeg re-runs parse→lower from canonical_text and asserts the
// lowered definition rehashes to the stored address (ADR-02 §5 pipeline leg,
// guarantee 4 over the historical corpus). Imports resolve against the live
// catalog exactly as admission does (pipeline.go).
func checkDefPipelineLeg(ctx context.Context, conn *pgwire.Conn, name, hash, text string) string {
	module := name
	local := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		module, local = name[:i], name[i+1:]
	}
	var serErr error
	resolver := func(qualified string) (string, bool) {
		qn := qualifiedToCatalogName(qualified)
		r, ok, err := catalog.Resolve(ctx, conn, catalog.ResolveReq{Name: qn, CallerModule: ""})
		if err != nil {
			serErr = err
			return "", false
		}
		if !ok {
			return "", false
		}
		return r.Hash, true
	}
	res := lower.Module(text, lower.ModuleContext{ModuleName: module, Resolve: resolver})
	if serErr != nil {
		return "resolver error during re-lower: " + serErr.Error()
	}
	if !res.OK() {
		var codes []string
		for _, dg := range res.Diagnostics {
			codes = append(codes, dg.Code)
		}
		return "canonical_text no longer lowers: " + strings.Join(codes, ",")
	}
	for _, d := range res.Definitions {
		if d.Name == local {
			if d.Hash != hash {
				return fmt.Sprintf("canonical_text re-lowers to %s, not its address %s (parse/lower drift)", d.Hash[:16], hash[:16])
			}
			return ""
		}
	}
	return "canonical_text no longer produces the definition " + local
}

// emitScrubberTripped writes the ADR-13 §2 #16 store.scrubber_tripped event on
// the Postgres-independent stdout channel (ADR-13 §4) — one JSON line per trip,
// machine-parseable, carrying the (address, leg) alarm payload.
func emitScrubberTripped(findings []CanaryFinding) {
	ev := map[string]any{
		"event": "store.scrubber_tripped",
		"count": len(findings),
		"pairs": findings,
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(ev)
	fmt.Fprintln(os.Stdout, string(b))
}

// hexToBytes decodes the hex text Postgres' encode(...,'hex') produces (used by
// the migrate continuation scan).
func hexToBytes(s string) ([]byte, error) { return hex.DecodeString(s) }
