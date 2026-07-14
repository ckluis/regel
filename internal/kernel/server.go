// Package kernel is the minimal Stage-A HTTP reactor (ADR-06 §4 subset,
// STAGE-A-PLAN pin #5): admit, eval (with as-of + tiered budgets), 202-on-park,
// and the restart endpoint. It owns the process-wide interpreter (backed by the
// catalog) and the genesis native registry.
package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/gitproj"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// Server is the HTTP kernel. One interpreter, one pool, the genesis image.
type Server struct {
	pool     *pgwire.Pool
	interp   *cek.Interp
	image    *admission.Image
	kernelID string
	epoch    int             // pinned catalog epoch, read at boot after VerifyBoot (ADR-06 §6)
	draining atomic.Bool     // set by the epoch fence: 503 on new work
	mirror   *gitproj.Mirror // optional ADR-09 git projection mirror (nil ⇒ no projection)
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
	if verr != nil {
		pool.Release(conn)
		return nil, verr
	}
	// Pin the live catalog epoch AFTER boot parity (ADR-06 §6): every work txn
	// fences against this value.
	var epoch int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &epoch); err != nil {
		pool.Release(conn)
		return nil, err
	}
	pool.Release(conn)
	src := &catalogSource{pool: pool}
	interp := cek.New(src, image.Registry())
	return &Server{pool: pool, interp: interp, image: image, kernelID: admissionUUID(), epoch: epoch}, nil
}

// SetMirror wires an ADR-09 git projection mirror. After a green admission the
// serve path folds the ledger and advances the mirror (self-healing). nil disables
// projection (the default), so kernels without a configured mirror are unaffected.
func (s *Server) SetMirror(m *gitproj.Mirror) { s.mirror = m }

// Draining reports whether the epoch fence has tripped and the kernel is in
// terminal drain (ADR-06 §6): new work is refused with 503.
func (s *Server) Draining() bool { return s.draining.Load() }

// Epoch returns the kernel's pinned catalog epoch.
func (s *Server) Epoch() int { return s.epoch }

// KernelID returns the kernel's ephemeral lease-owner uuid.
func (s *Server) KernelID() string { return s.kernelID }

// Interp exposes the interpreter (StartWorkflow / step-once need InitialState).
func (s *Server) Interp() *cek.Interp { return s.interp }

// stepEnv builds the StepEnv for this kernel's fenced work transactions.
func (s *Server) stepEnv(leaseSeconds int) cfr.StepEnv {
	return cfr.StepEnv{KernelID: s.kernelID, KernelEpoch: s.epoch, LeaseSeconds: leaseSeconds}
}

// Handler returns the routed HTTP handler (Go 1.22+ method+wildcard patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admit", s.handleAdmit)
	mux.HandleFunc("POST /eval/{name...}", s.handleEval)
	mux.HandleFunc("POST /workflow/{name...}", s.handleStartWorkflow)
	mux.HandleFunc("GET /continuation/{id}", s.handleContinuation)
	mux.HandleFunc("POST /continuation/{id}/restart", s.handleRestart)
	mux.HandleFunc("POST /channel/{channel}/send", s.handleChannelSend)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
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
