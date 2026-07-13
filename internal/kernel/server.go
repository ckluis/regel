// Package kernel is the minimal Stage-A HTTP reactor (ADR-06 §4 subset,
// STAGE-A-PLAN pin #5): admit, eval (with as-of + tiered budgets), 202-on-park,
// and the restart endpoint. It owns the process-wide interpreter (backed by the
// catalog) and the genesis native registry.
package kernel

import (
	"context"
	"encoding/json"
	"net/http"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// Server is the HTTP kernel. One interpreter, one pool, the genesis image.
type Server struct {
	pool     *pgwire.Pool
	interp   *cek.Interp
	image    *admission.Image
	kernelID string
}

// New builds a kernel over a live pool. It verifies boot parity (ADR-10 §2:
// std-manifest root + dispatch attestation match the pinned epoch) before
// opening the gate, then wires the interpreter to the catalog and the genesis
// registry.
func New(ctx context.Context, pool *pgwire.Pool) (*Server, error) {
	image := admission.BuildImage()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	verr := admission.VerifyBoot(ctx, conn, image)
	pool.Release(conn)
	if verr != nil {
		return nil, verr
	}
	src := &catalogSource{pool: pool}
	interp := cek.New(src, image.Registry())
	return &Server{pool: pool, interp: interp, image: image, kernelID: admissionUUID()}, nil
}

// Handler returns the routed HTTP handler (Go 1.22+ method+wildcard patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admit", s.handleAdmit)
	mux.HandleFunc("POST /eval/{name...}", s.handleEval)
	mux.HandleFunc("GET /continuation/{id}", s.handleContinuation)
	mux.HandleFunc("POST /continuation/{id}/restart", s.handleRestart)
	return mux
}

// Serve runs the HTTP kernel on addr until an unrecoverable error.
func (s *Server) Serve(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	return srv.ListenAndServe()
}

// ParseArgsJSON decodes a JSON array of arguments into cek Values (CLI helper).
func ParseArgsJSON(body []byte) ([]cek.Value, error) { return parseArgs(body) }

// catalogSource loads definition ASTs from the catalog for the interpreter. The
// interpreter caches by content hash (immortal rows), so this hits the DB once
// per definition.
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
		return nil, errNotFound(hash)
	}
	return rast.Decode(def.AST)
}

type notFoundError struct{ hash string }

func (e notFoundError) Error() string { return "kernel: definition not found: " + e.hash }
func errNotFound(hash string) error   { return notFoundError{hash} }

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// admissionUUID mints a v4 UUID for the kernel lease owner id.
func admissionUUID() string { return admission.NewUUID() }
