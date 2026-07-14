// Package mcp is the regel kernel's MCP/agent plane (ADR-12): a thin door over the
// same internal functions the HTTP and CLI doors use (one gate, N doors — no second
// pipeline). It speaks an owned, minimal JSON-RPC 2.0 (zero third-party deps) over
// stdio AND an HTTP door sharing one dispatch, exposing 11 tools, 6 resources, and
// 3 prompts. Every name-addressed read runs the ADR-12 §3 visibility predicate
// FIRST and shares one fast-fail NOT_FOUND path padded to a fixed latency floor, so
// a name the caller cannot see is indistinguishable — in bytes and in time — from a
// name that never existed.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// ResolutionFloor is the fixed latency floor (ADR-12 §3) every NOT_FOUND on a
// name-addressed read is padded to, so a real out-of-scope name and a hallucinated
// name — which short-circuit down the SAME fast-fail path — cannot be told apart
// through the clock. A var so the timing red-path can prove it load-bearing:
// bypass it (0) with a seeded fast-path leak and the two-sample test separates the
// distributions (reds); restore it and they are indistinguishable.
var ResolutionFloor = 4 * time.Millisecond

// leakOutOfScope, when set (timing red-path only), makes a not-visible name do the
// extra row-fetch work the NAIVE resolver would (an existence oracle through the
// clock). With the floor ON the pad masks it; with the floor OFF the two-sample
// test detects it — proving both the floor and the test are load-bearing.
var leakOutOfScope bool

// Server is the MCP door over a live pool. It holds the genesis image and an
// interpreter (for the human/operator condition.restart resume); it owns no state
// an agent can drift — identity, gate, conditions, and continuations are reused.
type Server struct {
	pool   *pgwire.Pool
	image  *admission.Image
	interp *cek.Interp
	epoch  int
}

// New builds an MCP server over a pool, verifying boot parity first (like the
// kernel) so the door never opens on an unattested dispatch table.
func New(ctx context.Context, pool *pgwire.Pool) (*Server, error) {
	image := admission.BuildImage()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err := admission.VerifyBoot(ctx, conn, image); err != nil {
		pool.Release(conn)
		return nil, err
	}
	var epoch int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &epoch); err != nil {
		pool.Release(conn)
		return nil, err
	}
	pool.Release(conn)
	interp := cek.New(&catalogSource{pool: pool}, image.Registry())
	return &Server{pool: pool, image: image, interp: interp, epoch: epoch}, nil
}

// catalogSource loads definition ASTs for the interpreter (mirrors the kernel's).
type catalogSource struct{ pool *pgwire.Pool }

func (c *catalogSource) Load(hash string) (*rast.Node, error) {
	ctx := context.Background()
	conn, err := c.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer c.pool.Release(conn)
	def, ok, err := catalog.LoadDefinition(ctx, conn, hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFoundHash(hash)
	}
	return rast.Decode(def.AST)
}

type notFoundHash struct{ h string }

func (e notFoundHash) Error() string { return "mcp: definition not found: " + e.h }
func errNotFoundHash(h string) error { return notFoundHash{h} }

// --- principal (agent-key auth + rotation) -----------------------------------

// Session binds one MCP connection to its presented API key. The principal is
// re-resolved from agent_key on EVERY request (rotation: revoking the key refuses
// the next request; past admissions stay attributed — ADR-12 §1).
type Session struct {
	APIKey string
}

// HashKey is the sha256-hex an API key is stored under in agent_key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// resolvePrincipal re-reads agent_key for the session's key. A missing or revoked
// key authenticates to nothing (ok=false ⇒ every tool refuses).
func (s *Server) resolvePrincipal(ctx context.Context, conn *pgwire.Conn, sess *Session) (admission.Principal, bool, error) {
	if sess == nil || sess.APIKey == "" {
		return admission.Principal{}, false, nil
	}
	var kind, id, scopeID string
	var scopeKind int
	var revoked bool
	found, err := conn.QueryRow(ctx, `
SELECT actor_kind, actor_id, scope_kind, scope_id, revoked
FROM agent_key WHERE key_hash=$1`,
		[]any{HashKey(sess.APIKey)}, &kind, &id, &scopeKind, &scopeID, &revoked)
	if err != nil || !found || revoked {
		return admission.Principal{}, false, err
	}
	p := admission.Principal{ActorKind: kind, ActorID: id, Via: "mcp"}
	// The agent's overlay (sandbox org) scope becomes its read/patch scope chain.
	switch scopeKind {
	case 2:
		p.Chain = catalog.Chain{OrgID: scopeID}
	case 3:
		p.Chain = catalog.Chain{TeamID: scopeID}
	case 4:
		p.Chain = catalog.Chain{UserID: scopeID}
	case 1:
		p.Chain = catalog.Chain{PackageID: scopeID}
	}
	return p, true, nil
}

// --- scope + qname (ADR-12 §2: one scoped-name grammar) ----------------------

// visibleScopes is the caller's scope set (ADR-03 §3): product plus each non-empty
// chain level. Membership is evaluated FIRST on every name-addressed read (§3).
func visibleScopes(chain catalog.Chain) []admission.Scope {
	out := []admission.Scope{{Kind: 0, ID: ""}}
	if chain.PackageID != "" {
		out = append(out, admission.Scope{Kind: 1, ID: chain.PackageID})
	}
	if chain.OrgID != "" {
		out = append(out, admission.Scope{Kind: 2, ID: chain.OrgID})
	}
	if chain.TeamID != "" {
		out = append(out, admission.Scope{Kind: 3, ID: chain.TeamID})
	}
	if chain.UserID != "" {
		out = append(out, admission.Scope{Kind: 4, ID: chain.UserID})
	}
	return out
}

// scopeVisible reports whether a (kind,id) scope is in the caller's visible set.
func scopeVisible(chain catalog.Chain, kind int, id string) bool {
	for _, s := range visibleScopes(chain) {
		if s.Kind == kind && s.ID == id {
			return true
		}
	}
	return false
}

// scopeToken renders a scope as the dotted qname scope path (ADR-12 §2).
func scopeToken(kind int, id string) string {
	switch kind {
	case 0:
		return "product"
	case 1:
		return "package." + id
	case 2:
		return "org." + id
	case 3:
		return "team." + id
	case 4:
		return "user." + id
	}
	return "product"
}

// parseScopeToken parses a qname scope path back to (kind, id).
func parseScopeToken(tok string) (int, string, bool) {
	if tok == "product" {
		return 0, "", true
	}
	i := strings.IndexByte(tok, '.')
	if i < 0 {
		return 0, "", false
	}
	prefix, id := tok[:i], tok[i+1:]
	switch prefix {
	case "package":
		return 1, id, true
	case "org":
		return 2, id, true
	case "team":
		return 3, id, true
	case "user":
		return 4, id, true
	}
	return 0, "", false
}

// makeQName builds the canonical qname := name "@" scope (ADR-12 §2).
func makeQName(name string, kind int, id string) string {
	return name + "@" + scopeToken(kind, id)
}

// parseQName splits a qname into (name, scopeKind, scopeId). The scope token is the
// substring after the LAST '@' (names never contain '@').
func parseQName(q string) (name string, kind int, id string, ok bool) {
	i := strings.LastIndexByte(q, '@')
	if i < 0 {
		return "", 0, "", false
	}
	k, sid, sok := parseScopeToken(q[i+1:])
	if !sok {
		return "", 0, "", false
	}
	return q[:i], k, sid, true
}

// --- latency floor (ADR-12 §3) -----------------------------------------------

// floorNotFound pads a NOT_FOUND reply to the fixed resolution-latency floor
// (ADR-12 §3), so a sub-floor difference between not-visible and not-exist cannot
// leak an existence signal through the clock. Called on EVERY name-addressed
// NOT_FOUND, down the one shared fast-fail path.
func floorNotFound(start time.Time) {
	if ResolutionFloor <= 0 {
		return
	}
	if d := ResolutionFloor - time.Since(start); d > 0 {
		time.Sleep(d)
	}
}
