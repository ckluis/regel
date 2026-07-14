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
	"regel.dev/regel/internal/mcp"
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
	case "mcp":
		err = cmdMCP(args)
	case "approve":
		err = cmdApprove(args)
	case "agent-key":
		err = cmdAgentKey(args)
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
        [--lease SECONDS] [--poll DUR]  lease window + reactor poll interval
  regel step-once [--lease N] CONT-ID   claim + step one continuation once (probe)
  regel admit FILE... --name-prefix P   admit source through the gate (prints Verdict)
        [--actor kind:id] [--declare c1,c2] [--tier trusted|sandbox]
        [--base name=hash ...]
  regel eval NAME [ARGS_JSON]           resolve + evaluate (prints value)
        [--as-of RFC3339] [--tier sandbox --fuel N]
  regel grant SUBJECT CAPABILITY        dev helper: insert a grant_row
  regel mcp [--key KEY]                  run the MCP/agent plane over stdio (JSON-RPC 2.0)
  regel approve --for AGENT --hash H...  mint a one-shot product-scope approval token
        [--minter kind:id] [--scope S] [--ttl SECONDS]
  regel agent-key --key KEY --actor a1   dev helper: bind an API key to an agent principal
        [--scope-id ORG] [--kind agent] [--revoke]

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
	lease := fs.Int("lease", 30, "continuation/task lease seconds (reaper recovery window)")
	poll := fs.Duration("poll", 250*time.Millisecond, "reactor poll interval")
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
	reactor := srv.StartReactor(ctx, kernel.ReactorConfig{
		LeaseSeconds: *lease,
		PollInterval: *poll,
		// A fast lease needs a fast reaper cadence to re-offer within it.
		ReapEvery:      reapCadence(*lease),
		HeartbeatEvery: heartbeatCadence(*lease),
	})
	defer reactor.Stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(*addr) }()
	fmt.Printf("serve: regel kernel %s listening on %s (epoch %d, lease %ds, poll %s)\n",
		srv.KernelID(), *addr, srv.Epoch(), *lease, *poll)
	select {
	case <-ctx.Done():
		fmt.Println("serve: signal received, draining reactor")
		return nil
	case e := <-errCh:
		return e
	}
}

// reapCadence keeps the reaper ticking well inside the lease window so a dead
// kernel's work is re-offered promptly (the kill-9 demo pins lease 2s). Capped at
// the 1s default so a long lease keeps the calm default cadence.
func reapCadence(leaseSecs int) time.Duration {
	d := time.Duration(leaseSecs) * time.Second / 4
	if d <= 0 || d > time.Second {
		return time.Second
	}
	if d < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	return d
}

// heartbeatCadence renews the lease at ~1/3 of its length (default 10s for the
// 30s lease), so a live kernel never lets its own lease lapse.
func heartbeatCadence(leaseSecs int) time.Duration {
	d := time.Duration(leaseSecs) * time.Second / 3
	if d <= 0 || d > 10*time.Second {
		return 10 * time.Second
	}
	if d < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return d
}

// --- step-once ---------------------------------------------------------------

// cmdStepOnce claims and steps one continuation exactly once, printing a JSON
// summary. Increment 3's cross-kernel determinism probe drives it.
func cmdStepOnce(args []string) error {
	fs := flag.NewFlagSet("step-once", flag.ExitOnError)
	lease := fs.Int("lease", 30, "claim lease seconds for this step")
	_ = fs.Parse(permute(args, map[string]bool{"lease": true}))
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("step-once: need CONTINUATION-ID")
	}
	contID := rest[0]
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
	env := cfr.StepEnv{KernelID: srv.KernelID(), KernelEpoch: srv.Epoch(), LeaseSeconds: *lease}
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

// --- mcp ---------------------------------------------------------------------

// cmdMCP runs the MCP/agent plane over stdio (JSON-RPC 2.0). The session's API key
// comes from --key or REGEL_MCP_KEY; it is re-resolved against agent_key on every
// request (rotation). No daemon is left running: the loop returns at stdin EOF.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	key := fs.String("key", os.Getenv("REGEL_MCP_KEY"), "agent API key (or REGEL_MCP_KEY)")
	_ = fs.Parse(args)

	cfg, err := pgwire.ParseDSN(dsn())
	if err != nil {
		return err
	}
	pool := pgwire.NewPool(cfg, 8)
	defer pool.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv, err := mcp.New(ctx, pool)
	if err != nil {
		return err
	}
	return srv.ServeStdio(ctx, &mcp.Session{APIKey: *key}, os.Stdin, os.Stdout)
}

// --- approve -----------------------------------------------------------------

// cmdApprove mints a one-shot product-scope approval token (ADR-12 §6/§7). The
// minter must hold the product.write capability. Prints the token.
func cmdApprove(args []string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	minter := fs.String("minter", "operator:human", "approving human principal kind:id")
	forAgent := fs.String("for", "", "author agent principal (kind:id)")
	scope := fs.String("scope", "product", "scope token to authorize (product|org.ID|...)")
	ttl := fs.Int("ttl", 3600, "token time-to-live seconds")
	var hashes multiFlag
	fs.Var(&hashes, "hash", "a content hash the token binds to (repeatable)")
	_ = fs.Parse(permute(args, map[string]bool{"minter": true, "for": true, "scope": true, "ttl": true, "hash": true}))

	if *forAgent == "" || len(hashes) == 0 {
		return fmt.Errorf("approve: need --for AGENT and at least one --hash")
	}
	sk, sid := scopeParts(*scope)

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	held, err := admission.HoldsProductWrite(ctx, conn, *minter)
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("approve: minter %q does not hold product.write", *minter)
	}
	token, err := admission.MintApprovalToken(ctx, conn, *minter, *forAgent,
		admission.Scope{Kind: sk, ID: sid}, hashes, time.Duration(*ttl)*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

// --- agent-key ---------------------------------------------------------------

// cmdAgentKey binds an API key to an agent principal + overlay scope (dev helper),
// or revokes it (--revoke) — the rotation path.
func cmdAgentKey(args []string) error {
	fs := flag.NewFlagSet("agent-key", flag.ExitOnError)
	key := fs.String("key", "", "the API key to bind")
	actor := fs.String("actor", "", "the agent actor id")
	kind := fs.String("kind", "agent", "actor kind")
	scopeID := fs.String("scope-id", "", "the agent's overlay (sandbox org) id")
	revoke := fs.Bool("revoke", false, "revoke this key (rotation)")
	_ = fs.Parse(permute(args, map[string]bool{"key": true, "actor": true, "kind": true, "scope-id": true}))

	if *key == "" {
		return fmt.Errorf("agent-key: need --key")
	}
	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if *revoke {
		if _, err := conn.Exec(ctx, `UPDATE agent_key SET revoked=true WHERE key_hash=$1`, mcp.HashKey(*key)); err != nil {
			return err
		}
		fmt.Println("agent-key: revoked")
		return nil
	}
	if *actor == "" {
		return fmt.Errorf("agent-key: need --actor")
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO agent_key (key_hash, actor_kind, actor_id, scope_kind, scope_id)
VALUES ($1, $2, $3, 2, $4)
ON CONFLICT (key_hash) DO UPDATE SET actor_kind=EXCLUDED.actor_kind, actor_id=EXCLUDED.actor_id,
  scope_id=EXCLUDED.scope_id, revoked=false`,
		mcp.HashKey(*key), *kind, *actor, *scopeID); err != nil {
		return err
	}
	fmt.Printf("agent-key: bound %s → %s:%s @org.%s\n", "sha256("+(*key)[:min(4, len(*key))]+"…)", *kind, *actor, *scopeID)
	return nil
}

// scopeParts parses a scope token to (kind, id) for the CLI.
func scopeParts(tok string) (int, string) {
	if tok == "product" || tok == "" {
		return 0, ""
	}
	if i := strings.IndexByte(tok, '.'); i >= 0 {
		switch tok[:i] {
		case "package":
			return 1, tok[i+1:]
		case "org":
			return 2, tok[i+1:]
		case "team":
			return 3, tok[i+1:]
		case "user":
			return 4, tok[i+1:]
		}
	}
	return 0, ""
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
