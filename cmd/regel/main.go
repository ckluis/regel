// Command regel is the one Stage-A binary: substrate migration, genesis, the
// HTTP kernel, and CLI doors for admission and evaluation (STAGE-A-PLAN,
// ADR-06/07/10). Go stdlib + the owned internal packages only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/gitproj"
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
	case "project":
		err = cmdProject(args)
	case "git-submit":
		os.Exit(cmdGitSubmit(args))
	case "git-identity":
		err = cmdGitIdentity(args)
	case "shred":
		err = cmdShred(args)
	case "vault-put":
		err = cmdVaultPut(args)
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
        [--spool DIR]                   outbox delivery file spool (default ./regel-spool)
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
  regel project --mirror PATH            fold the ledger into the bare mirror (ADR-09)
  regel git-submit --email E FILE...     submit changed files through the git door
        [--merge] [--mirror PATH]        (default dry-run PR check; --merge admits)
        [--base name=hash ...]
  regel git-identity --email E --actor a1  dev helper: bind a git identity to a principal
        [--scope-id S] [--kind engineer] [--scope-kind N] [--revoke]
  regel shred --resource NAME --subject ID  crypto-shred a subject's pii vault key
        [--scope S] [--by principal]        (ciphertext becomes undecryptable)
  regel vault-put --resource NAME --subject ID --field F   seal a pii value into the
        [--scope S]                         vault (AES-256-GCM); secret read from STDIN

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
	mirror := fs.String("mirror", "", "ADR-09 git projection bare-repo path (post-admission hook)")
	spool := fs.String("spool", "", "outbox delivery spool dir (default: ./regel-spool; hermetic file sink)")
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
	// ADR-06 §5 outbox delivery: wire a SAFE LOCAL sink by default (a hermetic
	// file/dir spool), so a serving kernel performs no real network I/O and demos
	// stay hermetic. Effectively-once is preserved (FileSink is idempotent under the
	// dedup key). A real HTTP sink (cfr.HTTPSink) exists for opt-in outbound
	// delivery; it is not the default because it would break demo hermeticity.
	spoolDir := *spool
	if spoolDir == "" {
		spoolDir = "regel-spool"
	}
	srv.SetDeliverySink(cfr.NewFileSink(spoolDir))
	fmt.Printf("serve: outbox delivery → file spool %s (hermetic; effectively-once)\n", spoolDir)
	if *mirror != "" {
		m, err := gitproj.NewMirror(*mirror, gitproj.Config{})
		if err != nil {
			return err
		}
		srv.SetMirror(m)
		// Bring the mirror current at boot (fold + self-heal) before serving.
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		head, aerr := m.Advance(ctx, conn)
		pool.Release(conn)
		if aerr != nil {
			return aerr
		}
		fmt.Printf("serve: git projection mirror %s at head %s\n", *mirror, head)
	}
	reactor := srv.StartReactor(ctx, kernel.ReactorConfig{
		LeaseSeconds: *lease,
		PollInterval: *poll,
		// A fast lease needs a fast reaper cadence to re-offer within it.
		ReapEvery:      reapCadence(*lease),
		HeartbeatEvery: heartbeatCadence(*lease),
	})
	defer reactor.Stop()

	// ADR-11 §5/§6: the reactive-layer loops (invalidation LISTEN + idle-TTL
	// session sweeper) must run in the serving kernel, not only under test —
	// without them a committed mutation NOTIFYs into the void and no other
	// session's SSE stream is ever patched (found by demo-reactive.sh step 9).
	stopSessions := srv.StartSessions(ctx)
	defer stopSessions()

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

// --- project (ADR-09 outbound fold) ------------------------------------------

// cmdProject folds the admission ledger into the kernel-owned bare mirror and
// advances refs/heads/main (self-healing any divergence). Prints the head SHA.
func cmdProject(args []string) error {
	fs := flag.NewFlagSet("project", flag.ExitOnError)
	mirror := fs.String("mirror", "", "bare-repo path to fold into")
	_ = fs.Parse(permute(args, map[string]bool{"mirror": true}))
	if *mirror == "" {
		return fmt.Errorf("project: need --mirror PATH")
	}
	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := gitproj.NewMirror(*mirror, gitproj.Config{})
	if err != nil {
		return err
	}
	head, err := m.Advance(ctx, conn)
	if err != nil {
		return err
	}
	fmt.Printf("project: mirror %s at head %s\n", m.Repo().Dir(), head)
	return nil
}

// --- git-submit (ADR-09 inbound door) ----------------------------------------

// cmdGitSubmit runs the git-submission door over changed files: dry-run PR check
// (default) or real merge (--merge). Each positional arg is a repo path, or
// repoPath=localFile when the on-disk name differs. Prints the Verdict.
func cmdGitSubmit(args []string) int {
	fs := flag.NewFlagSet("git-submit", flag.ExitOnError)
	email := fs.String("email", "", "verified git committer identity")
	merge := fs.Bool("merge", false, "admit for real (default: dry-run PR check)")
	mirror := fs.String("mirror", "", "bare-repo path to advance on a merge accept")
	var bases multiFlag
	fs.Var(&bases, "base", "expected head, name=hash (repeatable)")
	_ = fs.Parse(permute(args, map[string]bool{"email": true, "mirror": true, "base": true}))
	if *email == "" {
		fmt.Fprintln(os.Stderr, "git-submit: need --email")
		return 2
	}
	files := map[string]string{}
	for _, a := range fs.Args() {
		repoPath, local := a, a
		if i := strings.IndexByte(a, '='); i >= 0 {
			repoPath, local = a[:i], a[i+1:]
		}
		src, err := os.ReadFile(local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "git-submit: read %s: %v\n", local, err)
			return 1
		}
		files[repoPath] = string(src)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "git-submit: need at least one FILE")
		return 2
	}
	sub := gitproj.Submission{Files: files, Email: *email, Bases: map[string]string{}}
	for _, b := range bases {
		if i := strings.IndexByte(b, '='); i >= 0 {
			sub.Bases[b[:i]] = b[i+1:]
		}
	}

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-submit: connect: %v\n", err)
		return 1
	}
	defer conn.Close()

	var m *gitproj.Mirror
	if *mirror != "" {
		if m, err = gitproj.NewMirror(*mirror, gitproj.Config{}); err != nil {
			fmt.Fprintf(os.Stderr, "git-submit: mirror: %v\n", err)
			return 1
		}
	}
	im := admission.BuildImage()
	var v admission.Verdict
	if *merge {
		v, err = gitproj.Merge(ctx, conn, sub, im, m)
	} else {
		v, err = gitproj.DryRun(ctx, conn, sub, im)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-submit: %v\n", err)
		return 1
	}
	printJSON(v)
	if v.Outcome == admission.OutcomeAdmitted || v.Outcome == admission.OutcomeAlreadyAdmitted {
		return 0
	}
	return 1
}

// --- git-identity (dev helper) -----------------------------------------------

// cmdGitIdentity binds a verified git committer email to a catalog principal +
// scope (dev helper), or revokes it (--revoke) — the rotation path.
func cmdGitIdentity(args []string) error {
	fs := flag.NewFlagSet("git-identity", flag.ExitOnError)
	email := fs.String("email", "", "the verified git committer email")
	actor := fs.String("actor", "", "the catalog actor id")
	kind := fs.String("kind", "engineer", "actor kind")
	scopeKind := fs.Int("scope-kind", 0, "bind scope kind (0 product, 1 package, 2 org, ...)")
	scopeID := fs.String("scope-id", "", "bind scope id")
	revoke := fs.Bool("revoke", false, "revoke this identity")
	_ = fs.Parse(permute(args, map[string]bool{"email": true, "actor": true, "kind": true, "scope-kind": true, "scope-id": true}))

	if *email == "" {
		return fmt.Errorf("git-identity: need --email")
	}
	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if *revoke {
		if _, err := conn.Exec(ctx, `UPDATE git_identity SET revoked=true WHERE email=$1`, *email); err != nil {
			return err
		}
		fmt.Println("git-identity: revoked")
		return nil
	}
	if *actor == "" {
		return fmt.Errorf("git-identity: need --actor")
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO git_identity (email, actor_kind, actor_id, scope_kind, scope_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (email) DO UPDATE SET actor_kind=EXCLUDED.actor_kind, actor_id=EXCLUDED.actor_id,
  scope_kind=EXCLUDED.scope_kind, scope_id=EXCLUDED.scope_id, revoked=false`,
		*email, *kind, *actor, *scopeKind, *scopeID); err != nil {
		return err
	}
	fmt.Printf("git-identity: bound %s → %s:%s @scope %d:%s\n", *email, *kind, *actor, *scopeKind, *scopeID)
	return nil
}

// --- shred (ADR-10 §4 item 5 crypto-shred) -----------------------------------

// cmdShred crypto-shreds a data subject's pii vault key: it resolves the resource's
// derived table (the vault key), deletes the subject's key row, and writes an
// attestation in one transaction. After it commits the subject's ciphertext is
// permanently undecryptable and reads return the mask token.
func cmdShred(args []string) error {
	fs := flag.NewFlagSet("shred", flag.ExitOnError)
	resource := fs.String("resource", "", "derived resource name (e.g. app/crm/Contact)")
	subject := fs.String("subject", "", "the data subject's row id")
	scope := fs.String("scope", "product", "the resource scope (product|org.ID|...)")
	by := fs.String("by", "operator:cli", "the shredding principal")
	_ = fs.Parse(permute(args, map[string]bool{"resource": true, "subject": true, "scope": true, "by": true}))
	if *resource == "" || *subject == "" {
		return fmt.Errorf("shred: need --resource NAME and --subject ID")
	}
	sk, sid := scopeParts(*scope)

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// The vault keys on the derived physical table name (the stable per-resource key).
	var table string
	ok, err := conn.QueryRow(ctx,
		`SELECT table_name FROM derived_resource WHERE resource_name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{*resource, sk, sid}, &table)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("shred: no derived resource %q at scope %s", *resource, *scope)
	}
	attID, n, err := admission.CryptoShred(ctx, conn, table, *subject, *by)
	if err != nil {
		return err
	}
	fmt.Printf("shred: %s subject %s — %d key(s) destroyed, attestation #%d (ciphertext now undecryptable)\n",
		*resource, *subject, n, attID)
	return nil
}

// --- vault-put (ADR-10 §4 item 5 — the VaultPut CLI door) --------------------

// cmdVaultPut seals a pii value into the vault substrate through the REAL
// internal/admission.VaultPut (same per-subject AES-256-GCM AEAD the D1 test
// battery uses). The secret is read from STDIN, NEVER argv, so it never appears
// in the process table or shell history. It resolves the derived physical table
// name exactly as `regel shred` does (derived_resource), so the vault key
// (resource, subject_id, field) matches the read/shred path.
func cmdVaultPut(args []string) error {
	fs := flag.NewFlagSet("vault-put", flag.ExitOnError)
	resource := fs.String("resource", "", "derived resource name (e.g. app/crm/Contact)")
	subject := fs.String("subject", "", "the data subject's row id")
	field := fs.String("field", "", "the pii field name (e.g. email)")
	scope := fs.String("scope", "product", "the resource scope (product|org.ID|...)")
	_ = fs.Parse(permute(args, map[string]bool{"resource": true, "subject": true, "field": true, "scope": true}))
	if *resource == "" || *subject == "" || *field == "" {
		return fmt.Errorf("vault-put: need --resource NAME --subject ID --field F")
	}
	sk, sid := scopeParts(*scope)

	// The secret is read from stdin (never argv): the plaintext stays off the
	// process table and out of shell history.
	secret, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("vault-put: read stdin: %w", err)
	}
	plaintext := strings.TrimRight(string(secret), "\n")
	if plaintext == "" {
		return fmt.Errorf("vault-put: empty secret on stdin")
	}

	ctx, cancel := rootCtx()
	defer cancel()
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Resolve the derived physical table (the stable per-resource vault key),
	// identically to cmdShred.
	var table string
	ok, err := conn.QueryRow(ctx,
		`SELECT table_name FROM derived_resource WHERE resource_name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{*resource, sk, sid}, &table)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("vault-put: no derived resource %q at scope %s", *resource, *scope)
	}
	if err := admission.VaultPut(ctx, conn, table, *subject, *field, plaintext); err != nil {
		return err
	}
	fmt.Printf("vault-put: %s subject %s field %s — sealed (%d bytes plaintext, ciphertext-only in vault)\n",
		*resource, *subject, *field, len(plaintext))
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
