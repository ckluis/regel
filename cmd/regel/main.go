// Command regel is the one Stage-A binary: substrate migration, genesis, the
// HTTP kernel, and CLI doors for admission and evaluation (STAGE-A-PLAN,
// ADR-06/07/10). Go stdlib + the owned internal packages only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/kernel"
	"regel.dev/regel/internal/pgwire"
)

const defaultDSN = "postgres://clank@localhost:5432/regel"

func dsn() string {
	if d := os.Getenv("REGEL_PG_DSN"); d != "" {
		return d
	}
	return defaultDSN
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "migrate-db":
		err = cmdMigrate(args)
	case "genesis":
		err = cmdGenesis(args)
	case "serve":
		err = cmdServe(args)
	case "step-once":
		err = cmdStepOnce(args)
	case "admit":
		os.Exit(cmdAdmit(args))
	case "eval":
		err = cmdEval(args)
	case "grant":
		err = cmdGrant(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "regel: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "regel %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `regel — Stage-A walking skeleton

  regel migrate-db [--role NAME]        apply substrate DDL (+ optional kernel role)
  regel genesis                         admit the micro-std image + pin the epoch
  regel serve [--addr :8787]            run the HTTP kernel + reactor
  regel step-once CONTINUATION-ID       claim + step one continuation once (probe)
  regel admit FILE... --name-prefix P   admit source through the gate (prints Verdict)
        [--actor kind:id] [--declare c1,c2] [--tier trusted|sandbox]
        [--base name=hash ...]
  regel eval NAME [ARGS_JSON]           resolve + evaluate (prints value)
        [--as-of RFC3339] [--tier sandbox --fuel N]
  regel grant SUBJECT CAPABILITY        dev helper: insert a grant_row

  DSN via REGEL_PG_DSN (default `+defaultDSN+`)
`)
}

func connect(ctx context.Context) (*pgwire.Conn, error) {
	cfg, err := pgwire.ParseDSN(dsn())
	if err != nil {
		return nil, err
	}
	return pgwire.Connect(ctx, cfg)
}

func rootCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// --- migrate-db --------------------------------------------------------------

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate-db", flag.ExitOnError)
	role := fs.String("role", "", "optional least-privilege kernel role to create + grant")
	_ = fs.Parse(args)

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := catalog.Bootstrap(ctx, conn, *role); err != nil {
		return err
	}
	fmt.Println("migrate-db: substrate applied")
	return nil
}

// --- genesis -----------------------------------------------------------------

func cmdGenesis(args []string) error {
	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	im := admission.BuildImage()
	if err := admission.Genesis(ctx, conn, im); err != nil {
		return err
	}
	fmt.Printf("genesis: epoch %d pinned — std_manifest_root=%s dispatch_attestation=%s\n",
		im.Epoch, im.ManifestRoot[:16], im.Attestation[:16])
	return nil
}

// --- serve -------------------------------------------------------------------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8787", "listen address")
	_ = fs.Parse(args)

	cfg, err := pgwire.ParseDSN(dsn())
	if err != nil {
		return err
	}
	pool := pgwire.NewPool(cfg, 16)
	defer pool.Close()

	// Graceful shutdown on SIGTERM/SIGINT — but NOT SIGKILL: surviving kill -9 is
	// the point of Stage B (the reaper re-offers un-heartbeated leases).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := kernel.New(ctx, pool)
	if err != nil {
		return err
	}
	reactor := srv.StartReactor(ctx, kernel.ReactorConfig{})
	defer reactor.Stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(*addr) }()
	fmt.Printf("serve: regel kernel %s listening on %s (epoch %d)\n", srv.KernelID(), *addr, srv.Epoch())
	select {
	case <-ctx.Done():
		fmt.Println("serve: signal received, draining reactor")
		return nil
	case e := <-errCh:
		return e
	}
}

// --- step-once ---------------------------------------------------------------

// cmdStepOnce claims and steps one continuation exactly once, printing a JSON
// summary. Increment 3's cross-kernel determinism probe drives it.
func cmdStepOnce(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("step-once: need CONTINUATION-ID")
	}
	contID := args[0]
	cfg, err := pgwire.ParseDSN(dsn())
	if err != nil {
		return err
	}
	pool := pgwire.NewPool(cfg, 4)
	defer pool.Close()
	ctx, cancel := rootCtx()
	defer cancel()
	srv, err := kernel.New(ctx, pool)
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer pool.Release(conn)

	var seenSeq int64
	found, err := conn.QueryRow(ctx, `SELECT step_seq FROM continuation WHERE id=$1`, []any{contID}, &seenSeq)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("step-once: no such continuation %s", contID)
	}
	env := cfr.StepEnv{KernelID: srv.KernelID(), KernelEpoch: srv.Epoch(), LeaseSeconds: 30}
	resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
		return srv.Interp().Resume(ctx, st, d, p)
	}
	out, claimed, serr := cfr.ClaimAndStep(ctx, conn, env, srv.Interp(), contID, seenSeq, resume)
	summary := map[string]any{"claimed": claimed, "outcome": outcomeName(out.Kind)}
	if serr != nil {
		summary["error"] = serr.Error()
	}
	if v, ok, _ := cfr.LoadResult(ctx, conn, contID); ok {
		summary["result"] = kernel.ValueToJSON(v)
	}
	printJSON(summary)
	return nil
}

func outcomeName(k cek.OutcomeKind) string {
	switch k {
	case cek.OutDone:
		return "done"
	case cek.OutParked:
		return "parked"
	case cek.OutFaulted:
		return "faulted"
	default:
		return "error"
	}
}

// --- admit -------------------------------------------------------------------

func cmdAdmit(args []string) int {
	fs := flag.NewFlagSet("admit", flag.ExitOnError)
	namePrefix := fs.String("name-prefix", "", "catalog module path for the submitted file(s), e.g. app/demo")
	actor := fs.String("actor", "engineer:dev", "authenticated principal kind:id")
	declare := fs.String("declare", "", "comma-separated declared capabilities")
	tier := fs.String("tier", "trusted", "trusted|sandbox")
	var bases multiFlag
	fs.Var(&bases, "base", "expected head, name=hash (repeatable)")
	_ = fs.Parse(permute(args, map[string]bool{"name-prefix": true, "actor": true, "declare": true, "tier": true, "base": true}))

	files := fs.Args()
	if len(files) == 0 || *namePrefix == "" {
		fmt.Fprintln(os.Stderr, "admit: need FILE... and --name-prefix")
		return 2
	}

	patch := admission.Patch{
		TargetScope: admission.Scope{Kind: 0, ID: ""},
		BaseHashes:  map[string]string{},
		Tier:        map[string]string{},
	}
	if *declare != "" {
		patch.DefaultDeclared = splitComma(*declare)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "admit: read %s: %v\n", f, err)
			return 1
		}
		patch.Modules = append(patch.Modules, admission.ModuleSrc{ModuleName: *namePrefix, Source: string(src)})
	}
	for _, b := range bases {
		if i := strings.IndexByte(b, '='); i >= 0 {
			patch.BaseHashes[b[:i]] = b[i+1:]
		}
	}
	_ = *tier // Stage A: tier is an eval-request property (STAGE-A-PLAN pin #7)

	auth := parseActor(*actor)

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admit: connect: %v\n", err)
		return 1
	}
	defer conn.Close()
	v, err := admission.Admit(ctx, conn, patch, auth, admission.BuildImage())
	if err != nil {
		fmt.Fprintf(os.Stderr, "admit: %v\n", err)
		return 1
	}
	printJSON(v)
	if v.Outcome == admission.OutcomeAdmitted || v.Outcome == admission.OutcomeAlreadyAdmitted {
		return 0
	}
	return 1
}

// --- eval --------------------------------------------------------------------

func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	asOf := fs.String("as-of", "", "RFC3339 as-of instant")
	tier := fs.String("tier", "trusted", "trusted|sandbox")
	fuel := fs.Int64("fuel", 0, "sandbox fuel budget")
	_ = fs.Parse(permute(args, map[string]bool{"as-of": true, "tier": true, "fuel": true}))

	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("eval: need NAME")
	}
	name := rest[0]
	var argsJSON []byte
	if len(rest) > 1 {
		argsJSON = []byte(rest[1])
	}

	cfg, err := pgwire.ParseDSN(dsn())
	if err != nil {
		return err
	}
	pool := pgwire.NewPool(cfg, 4)
	defer pool.Close()
	ctx, cancel := rootCtx()
	defer cancel()
	srv, err := kernel.New(ctx, pool)
	if err != nil {
		return err
	}

	req := kernel.EvalRequest{Name: name, Tier: cek.TierTrusted}
	if *tier == "sandbox" {
		req.Tier = cek.TierSandbox
		req.Fuel = *fuel
	}
	if *asOf != "" {
		t, e := time.Parse(time.RFC3339, *asOf)
		if e != nil {
			return fmt.Errorf("bad --as-of: %w", e)
		}
		req.AsOf = &t
	}
	if len(argsJSON) > 0 {
		vals, e := kernel.ParseArgsJSON(argsJSON)
		if e != nil {
			return e
		}
		req.Args = vals
	}
	res, err := srv.Eval(ctx, req)
	if err != nil {
		return err
	}
	printJSON(res.Body)
	return nil
}

// --- grant -------------------------------------------------------------------

func cmdGrant(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("grant: need SUBJECT CAPABILITY")
	}
	subject, capability := args[0], args[1]
	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Exec(ctx, `
INSERT INTO grant_row (subject, capability, scope, granted_by)
VALUES ($1, $2, '', 'cli') ON CONFLICT (subject, capability, scope) DO NOTHING`,
		subject, capability)
	if err != nil {
		return err
	}
	fmt.Printf("grant: %s → %s\n", subject, capability)
	return nil
}

// --- shared helpers ----------------------------------------------------------

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// permute reorders args so all flags precede positionals, letting Go's flag
// package (which stops at the first non-flag) accept `admit FILE --flag V`.
// valueFlags names the flags that consume a following token.
func permute(args []string, valueFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if j := strings.IndexByte(name, '='); j >= 0 {
				name = name[:j]
				continue
			}
			if valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func parseActor(s string) admission.Principal {
	kind, id := "engineer", s
	if i := strings.IndexByte(s, ':'); i >= 0 {
		kind, id = s[:i], s[i+1:]
	}
	return admission.Principal{ActorKind: kind, ActorID: id, Via: "cli"}
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}
