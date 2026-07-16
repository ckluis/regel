package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
	"regel.dev/regel/internal/tsx"
)

// maxRetries bounds SERIALIZABLE retries (ADR-07 §6): after this many
// serialization failures the pipeline returns retry-exhausted.
const maxRetries = 3

// typecheckWall is the ADR-07 §3 secondary wall-clock backstop for the
// in-transaction typecheck (liveness only; the deterministic type-graph ceiling
// is the primary control). A var so tests may tighten it.
var typecheckWall = tsx.DefaultTypecheckWall

// typeGraphBudgetDiags runs the deterministic type-graph node ceiling (ADR-07 §3,
// BUILD-C) over every submitted module and returns a TYPECHECK_BUDGET diagnostic
// on the first breach, naming the offending site. Parsing a module is a pure
// function of its source, so the verdict is byte-identical on any kernel.
func typeGraphBudgetDiags(patch Patch) []Diagnostic {
	for _, mod := range patch.Modules {
		pr, err := tsx.Parse("/"+mod.ModuleName+".ts", mod.Source)
		if err != nil || pr == nil || pr.SourceFile == nil {
			continue // a parse fault is lowering's to report, not the budget's
		}
		if b := tsx.CheckTypeGraphBudget(pr.SourceFile); b != nil {
			kind := "nesting depth"
			if b.Kind == "count" {
				kind = "node count"
			}
			return []Diagnostic{{
				StageOrVerifier: "tsgo", Code: "TYPECHECK_BUDGET", Severity: "error",
				Subject: mod.ModuleName,
				Loc:     Loc{Span: b.Site},
				Message: "the submitted type graph exceeds the deterministic " + kind +
					" ceiling at " + b.Site + " (" + itoaAdm(b.Measured) + " > " + itoaAdm(b.Ceiling) +
					"); a conditional/mapped-type bomb is refused before the checker runs",
				Fix: "reduce type-level nesting/breadth (a deeply-recursive or excessively wide generic type) and resubmit",
			}}
		}
	}
	return nil
}

// itoaAdm is a tiny local int→string for diagnostic messages.
func itoaAdm(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

// loweredDef pairs a lowered definition with its full catalog name and module.
type loweredDef struct {
	CatalogName string
	Module      string
	Def         lower.Definition
}

// admitWithRetries runs the ADR-03 §5 / ADR-07 §1 admission pipeline (Stage-A
// subset) for one patch, over a dedicated connection, retrying the whole
// transaction on a serialization conflict up to maxRetries. It is the in/near-txn
// core; the exported Admit door (backpressure.go) wraps it with the pre-BEGIN
// admission-control semaphore (busy) and per-principal fuel bucket (budget). A
// non-nil error is an internal fault; every ordinary refusal is a Verdict.
func admitWithRetries(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image, commit bool) (Verdict, error) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		v, retry, err := admitOnce(ctx, conn, patch, auth, im, commit)
		if err != nil {
			return Verdict{}, err
		}
		if retry {
			admitRetries.Add(1) // ADR-07 §3 R1-07 benchmark: serialization-retry count
			continue
		}
		return v, nil
	}
	// Retries exhausted: a transient give-up (ADR-07 §6). Durable refusal.
	v := Verdict{
		Outcome:      OutcomeRetryExhausted,
		Hashes:       map[string]string{},
		Stages:       []Stage{{Stage: "serialize", Status: "fail"}},
		Epoch:        im.Epoch,
		BaseSnapshot: time.Now().UTC().Format(time.RFC3339Nano),
		RetryAfter:   &RetryAfter{Millis: 25, Cause: "serialization"},
		Diagnostics: []Diagnostic{{
			StageOrVerifier: "serialize", Code: "SERIALIZATION_RETRY_EXHAUSTED", Severity: "error",
			Message: "admission lost the serialization race more than the retry budget allows; re-read the head and resubmit",
		}},
	}
	if err := finishRefusal(ctx, conn, auth, patch, &v); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

// admitOnce is one attempt of the one-SERIALIZABLE-transaction pipeline. It
// returns (verdict, retry, err): retry=true means a serialization failure
// rolled the whole attempt back and the caller should try a fresh snapshot.
func admitOnce(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image, commit bool) (Verdict, bool, error) {
	base := time.Now().UTC().Format(time.RFC3339Nano)
	v := Verdict{Hashes: map[string]string{}, Epoch: im.Epoch, BaseSnapshot: base, DryRun: !commit}
	var stages []Stage
	stageStart := time.Now()
	mark := func(name, status string) {
		stages = append(stages, Stage{Stage: name, Status: status, Ms: time.Since(stageStart).Milliseconds()})
		stageStart = time.Now()
	}

	if err := conn.BeginSerializable(ctx); err != nil {
		return Verdict{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()

	// --- 2a bind scope/principal (from auth, never the body) -----------------
	scope := patch.TargetScope // default {0, ""} = product

	// --- typecheck budget: deterministic type-graph node ceiling (ADR-07 §3,
	//     BUILD-C owned-seam realization). A pure syntactic pre-check over the
	//     submitted type surface, ahead of the checker (and, since it is pure,
	//     ahead of lowering): the same submission ⇒ the same TYPECHECK_BUDGET
	//     verdict on any machine. Breach is the normal in-txn reject path. --------
	if bdiags := typeGraphBudgetDiags(patch); len(bdiags) > 0 {
		mark("typecheck-budget", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, bdiags)
	}

	// --- 2b/2c lower each module against this txn snapshot --------------------
	lowered, diags, serr := lowerPatch(ctx, conn, patch, scope, im)
	if serr != nil {
		if isSerialization(serr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, serr
	}
	for _, ld := range lowered {
		v.Hashes[ld.CatalogName] = ld.Def.Hash
	}
	if len(diags) > 0 {
		mark("lower", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, diags)
	}
	mark("lower", "pass")

	// --- 2a content-seeder set: bound from the read-log, validated against the
	//     AUTHENTICATED principal's scope chain. An out-of-chain seeder is
	//     unrepresentable — rejected before any row is written (zero trace). ------
	seeders, seedBad := validateSeeders(patch, auth)
	if seedBad != nil {
		mark("seeders", "fail")
		v.Stages = stages
		v.Seeders = []Seeder{}
		return reject(ctx, conn, auth, patch, v, []Diagnostic{*seedBad})
	}
	v.Seeders = seeders

	// --- ADR-12 §6 patch scope policy: overlay self-serve; product by one-shot
	//     human approval token. Scope binds from the AUTHENTICATED principal, never
	//     the body — an agent may target only its own overlay scope; product
	//     requires a valid token, else CAP_UNGRANTED (escalation is evidence: a
	//     refusal-ledger row naming principal/scope/hashes, never a silent 403).
	//     Enforced before any row is written (zero trace on refusal). --------------
	tokenToConsume, scopeDiag, spErr := checkScopePolicy(ctx, conn, patch, auth, scope, lowered)
	if spErr != nil {
		if isSerialization(spErr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, spErr
	}
	if scopeDiag != nil {
		mark("scope-policy", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, []Diagnostic{*scopeDiag})
	}
	if tokenToConsume != nil {
		v.ApprovedBy = tokenToConsume.MintedBy
	}

	// Grants for the principal (loaded from grant_row at bind time — Stage A).
	grants, gerr := loadGrants(ctx, conn, auth.Subject())
	if gerr != nil {
		if isSerialization(gerr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, gerr
	}

	// --- 1 INSERT admission RETURNING id (audit spine) -----------------------
	hashes := make([]string, 0, len(lowered))
	for _, ld := range lowered {
		hashes = append(hashes, ld.Def.Hash)
	}
	admissionID, aerr := insertAdmission(ctx, conn, auth, hashes)
	if aerr != nil {
		if isSerialization(aerr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, aerr
	}

	// --- 2d no-op short-circuit ---------------------------------------------
	noop, nerr := isNoop(ctx, conn, lowered, scope)
	if nerr != nil {
		if isSerialization(nerr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, nerr
	}
	if noop {
		mark("already-admitted", "pass")
		v.Outcome = OutcomeAlreadyAdmitted
		v.AdmissionID = admissionID
		v.Stages = stages
		v.Diagnostics = []Diagnostic{}
		// A no-op changes nothing, so the delta adds nothing vs. base (empty
		// added_vs_base everywhere) — computed, not faked (ADR-07 §6, R1-04).
		v.Delta = Delta{}
		if err := recordReport(ctx, conn, admissionID, 0, stages, "", v); err != nil {
			if isSerialization(err) {
				return Verdict{}, true, nil
			}
			return Verdict{}, false, err
		}
		retry, ferr := finalizeTxn(ctx, conn, &v, commit, &committed)
		return v, retry, ferr
	}

	// --- 3 INSERT definition / definition_meta (dep order, rast re-hash) -----
	if err := insertDefinitions(ctx, conn, lowered, admissionID, im); err != nil {
		if isSerialization(err) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, err
	}
	mark("insert", "pass")

	// --- 4 tsgo typecheck (L0 std ⊕ L1 app ⊕ L2 patch) -----------------------
	files, roots, terr := buildTypecheckWorld(ctx, conn, im, patch, lowered, scope)
	if terr != nil {
		if isSerialization(terr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, terr
	}
	tcStart := time.Now()
	// Wall-clock deadline (ADR-07 §3 secondary backstop): a liveness net only —
	// the deterministic ceiling above refuses a bomb before this can fire. A
	// breach aborts the attempt cleanly with TYPECHECK_TIMEOUT (durable refusal).
	res, timedOut, cerr := tsx.TypecheckWithDeadline(tsx.CheckRequest{Files: files, RootFiles: roots}, typecheckWall)
	tsgoMs := time.Since(tcStart).Milliseconds()
	if cerr != nil && !timedOut {
		return Verdict{}, false, cerr
	}
	if timedOut {
		mark("tsgo", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, []Diagnostic{{
			StageOrVerifier: "tsgo", Code: "TYPECHECK_TIMEOUT", Severity: "error",
			Message: "typecheck exceeded its wall-clock deadline; the submission was aborted (serving traffic untouched). Reduce type-level complexity and resubmit",
			Fix:     "reduce type-level complexity (deeply-recursive or wide generic types) and resubmit",
		}})
	}
	if tdiags := typeErrorDiags(res, lowered); len(tdiags) > 0 {
		mark("tsgo", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, tdiags)
	}
	mark("tsgo", "pass")

	// --- 5a derivation seam (ADR-07 §1 step 5a): pure ordered passes over
	//     base ⊕ patch → proposed derived rows + migration_sql (nothing physical
	//     applied here); the proposed rows are stored so V3/V6 can query them. ----
	plan, derr := deriveResources(ctx, conn, lowered, patch, scope, im, admissionID)
	if derr != nil {
		if isSerialization(derr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, derr
	}
	mark("derive", "pass")

	// --- 5b verifier suite V1..V6 over base ⊕ patch ⊕ derived (in-txn) --------
	if vdiags := verifyV1(lowered, patch, grants, im); len(vdiags) > 0 {
		mark("V1", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V1", "pass")
	if !disableV2 {
		if vdiags := verifyV2(lowered, plan, im); len(vdiags) > 0 {
			mark("V2", "fail")
			v.Stages = stages
			return reject(ctx, conn, auth, patch, v, vdiags)
		}
	}
	mark("V2", "pass")
	if vdiags := verifyV3(plan); len(vdiags) > 0 {
		mark("V3", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V3", "pass")
	if !disableV4 {
		if vdiags := verifyV4(lowered, im); len(vdiags) > 0 {
			mark("V4", "fail")
			v.Stages = stages
			return reject(ctx, conn, auth, patch, v, vdiags)
		}
	}
	mark("V4", "pass")
	if !disableV5 {
		if vdiags := verifyV5(lowered, patch, im); len(vdiags) > 0 {
			mark("V5", "fail")
			v.Stages = stages
			return reject(ctx, conn, auth, patch, v, vdiags)
		}
	}
	mark("V5", "pass")
	if vdiags := verifyV6(plan); len(vdiags) > 0 {
		mark("V6", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V6", "pass")

	// --- blast-radius delta (ADR-07 §6, R1-04): a pure projection of what
	//     V1/V2/V6 already computed vs. the base snapshot. Computed BEFORE the CAS
	//     moves any pointer, so the base heads are still readable. ---------------
	touched := collectPiiTouched(lowered, im)
	delta, dlerr := computeDelta(ctx, conn, lowered, patch, grants, plan, touched, scope, im)
	if dlerr != nil {
		if isSerialization(dlerr) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, dlerr
	}
	v.Delta = delta

	// --- 6 apply migration_sql inside the txn (additive only — V6-enforced) ---
	if plan.MigrationSQL != "" {
		if _, err := conn.ExecSimple(ctx, plan.MigrationSQL); err != nil {
			if isSerialization(err) {
				return Verdict{}, true, nil
			}
			return Verdict{}, false, err
		}
	}
	mark("migrate", "pass")

	// --- 7 name_pointer CAS upsert per declaration ---------------------------
	for _, ld := range lowered {
		ptr := catalog.Pointer{
			Name:        ld.CatalogName,
			ScopeKind:   scope.Kind,
			ScopeID:     scope.ID,
			Kind:        catalogKind(ld.Def.Kind),
			Visibility:  visibilityOf(ld.Def.Exported),
			Hash:        ld.Def.Hash,
			AdmissionID: admissionID,
		}
		var basePtr *string
		if b, ok := patch.BaseHashes[ld.CatalogName]; ok && b != "" {
			basePtr = &b
		}
		moved, err := catalog.UpsertPointerCAS(ctx, conn, ptr, basePtr)
		if err != nil {
			if isSerialization(err) {
				return Verdict{}, true, nil
			}
			return Verdict{}, false, err
		}
		if !moved {
			mark("cas", "fail")
			v.Stages = stages
			return staleBase(ctx, conn, auth, patch, v, ld.CatalogName)
		}
	}
	mark("cas", "pass")

	// Consume the one-shot approval token inside the admission txn (ADR-12 §6): a
	// CAS on consumed_by binds it to THIS admission. 0 rows ⇒ the token was already
	// consumed (replay) ⇒ the whole admission is refused (rolled back, zero trace,
	// a refusal-ledger row). Only on commit; a dry-run rolls the txn back regardless
	// so it never consumes a token (an agent may dry-run freely against approval).
	if commit && tokenToConsume != nil {
		ok, cerr := consumeApprovalToken(ctx, conn, tokenToConsume.Token, admissionID)
		if cerr != nil {
			if isSerialization(cerr) {
				return Verdict{}, true, nil
			}
			return Verdict{}, false, cerr
		}
		if !ok {
			mark("approval", "fail")
			v.Stages = stages
			return reject(ctx, conn, auth, patch, v, []Diagnostic{approvalReplayDiag()})
		}
	}
	mark("approval", "pass")

	// Complete the Verdict, then persist it whole to the committed admission row —
	// the full green Verdict (delta + seeders) is retrievable later by admission id
	// from admission.verifier_report (symmetric with gate_refusal.verdict for red).
	v.Outcome = OutcomeAdmitted
	v.Admitted = true
	v.AdmissionID = admissionID
	v.Stages = stages
	v.Diagnostics = []Diagnostic{}
	if err := recordReport(ctx, conn, admissionID, tsgoMs, stages, plan.MigrationSQL, v); err != nil {
		if isSerialization(err) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, err
	}

	retry, ferr := finalizeTxn(ctx, conn, &v, commit, &committed)
	return v, retry, ferr
}

// finalizeTxn commits (commit:true) or rolls back (dry-run) the admission txn and
// marks it settled so the deferred rollback is a no-op. On a dry-run it zeroes the
// AdmissionID: the row was rolled back, so the returned Verdict must not claim a
// persisted admission (the meaningful fields stay byte-identical to the commit
// path — ADR-12 dry-run parity). A serialization loss on commit is a clean retry.
func finalizeTxn(ctx context.Context, conn *pgwire.Conn, v *Verdict, commit bool, committed *bool) (bool, error) {
	if !commit {
		if err := conn.Rollback(ctx); err != nil {
			return false, err
		}
		*committed = true
		v.AdmissionID = 0
		return false, nil
	}
	if err := conn.Commit(ctx); err != nil {
		if isSerialization(err) {
			return true, nil
		}
		return false, err
	}
	*committed = true
	return false, nil
}

// --- lowering ----------------------------------------------------------------

// lowerPatch lowers every module of the patch against a resolver bound to this
// txn snapshot plus the names admitted earlier in the same patch. It returns the
// lowered defs (in dependency-insert order) or the grammar/lowering diagnostics.
func lowerPatch(ctx context.Context, q catalog.Querier, patch Patch, scope Scope, im *Image) ([]loweredDef, []Diagnostic, error) {
	inPatch := map[string]string{} // catalog name → hash (earlier patch modules)
	var out []loweredDef
	var serErr error

	resolver := func(qualified string) (string, bool) {
		name := qualifiedToCatalogName(qualified)
		if h, ok := inPatch[name]; ok {
			return h, true
		}
		// Product-scope resolution against the txn snapshot (std + admitted app).
		// STAGE-A RESIDUE: imports resolve at product scope with an external
		// caller module (exported-only); private cross-module imports and
		// overlay-scope import resolution are Stage B.
		r, ok, err := catalog.Resolve(ctx, q, catalog.ResolveReq{Name: name, CallerModule: ""})
		if err != nil {
			serErr = err
			return "", false
		}
		if !ok {
			// MUTANT RESOLVER_ADMIT_OUT_OF_WORLD (ADR-07 §5 dir-ii, R1-10): the real
			// resolver refuses an import outside the catalog world (an unresolvable /
			// squatted name, or a stub-only surface with no admitted definition).
			// Weakening it to fall back to an in-world sentinel binds the out-of-world
			// import to a real hash so it "resolves" — a silently-admitted out-of-world
			// import the hostile corpus must kill.
			if mutants.Active("RESOLVER_ADMIT_OUT_OF_WORLD") {
				return outOfWorldSentinel(im), true
			}
			return "", false
		}
		return r.Hash, true
	}

	for _, mod := range patch.Modules {
		res := lower.Module(mod.Source, lower.ModuleContext{ModuleName: mod.ModuleName, Resolve: resolver})
		if serErr != nil {
			return nil, nil, serErr
		}
		if !res.OK() {
			return nil, convertLowerDiags(res.Diagnostics, mod.ModuleName), nil
		}
		for _, d := range res.Definitions {
			cn := mod.ModuleName + "/" + d.Name
			inPatch[cn] = d.Hash
			out = append(out, loweredDef{CatalogName: cn, Module: mod.ModuleName, Def: d})
		}
	}
	// Sort into dependency-insert order so I2 (deps must already exist) holds.
	return depInsertOrder(out), nil, nil
}

// depInsertOrder topologically orders lowered defs so every same-patch dep is
// inserted before its dependent (ADR-03 I2). The graph is a DAG (lowering
// rejects DEP_CYCLE); on any anomaly the input order is returned unchanged.
func depInsertOrder(defs []loweredDef) []loweredDef {
	inPatch := map[string]int{} // hash → index
	for i, d := range defs {
		inPatch[d.Def.Hash] = i
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, len(defs))
	var out []loweredDef
	var ok = true
	var visit func(i int)
	visit = func(i int) {
		if color[i] == black {
			return
		}
		if color[i] == gray {
			ok = false
			return
		}
		color[i] = gray
		deps := append([]rast.Dep(nil), defs[i].Def.Deps...)
		sort.Slice(deps, func(a, b int) bool { return deps[a].Hash < deps[b].Hash })
		for _, dep := range deps {
			if j, in := inPatch[dep.Hash]; in && j != i {
				visit(j)
			}
		}
		color[i] = black
		out = append(out, defs[i])
	}
	for i := range defs {
		visit(i)
	}
	if !ok || len(out) != len(defs) {
		return defs
	}
	return out
}

// outOfWorldSentinel returns a stable in-world std TYPE hash, used ONLY by the
// RESOLVER_ADMIT_OUT_OF_WORLD mutant to fabricate a resolution for an
// out-of-world import. Every std type shares the opaque genesis body (image.go),
// so the first type entry's hash is a real, FK-safe catalog def.
func outOfWorldSentinel(im *Image) string {
	for _, e := range im.Entries {
		if e.DefKind == rast.DefType {
			return e.Hash
		}
	}
	return ""
}

func visibilityOf(exported bool) string {
	if exported {
		return "exported"
	}
	return "private"
}

// --- typecheck world ---------------------------------------------------------

// buildTypecheckWorld assembles the hermetic tsgo world (ADR-07 §2): L0 std type
// surface ⊕ L1 catalogued app definitions ⊕ L2 the in-flight patch modules.
//
// STAGE-A RESIDUE: L1 serves one file per definition-name at "/"+catalog.NamePath
// (the ONE name→path function, shared with the ADR-09 projector — one function,
// two consumers, so typecheck layout and projection layout can never disagree).
// App→app imports that address a MODULE aggregate are a Stage-B concern; Stage-A
// patches import only std (served by L0), so unimported L1 files are inert under
// bundler resolution. L2 typechecks the submitted source rather than re-rendered
// canonical text.
func buildTypecheckWorld(ctx context.Context, q catalog.Querier, im *Image, patch Patch, lowered []loweredDef, scope Scope) (map[string]string, []string, error) {
	files := map[string]string{}
	// L0
	for path, text := range im.ModuleStubs {
		files[path] = text
	}
	// L2 (recorded first so we can exclude these names from L1)
	inPatchNames := map[string]bool{}
	for _, ld := range lowered {
		inPatchNames[ld.CatalogName] = true
	}
	var roots []string
	for _, mod := range patch.Modules {
		p := "/" + mod.ModuleName + ".ts"
		files[p] = mod.Source
		roots = append(roots, p)
	}
	// L1: catalogued app pointers at the target scope (minus the patch's own).
	rows, err := q.Query(ctx, `
SELECT np.name, d.canonical_text
FROM name_pointer np JOIN definition d ON d.hash = np.hash
WHERE np.scope_kind = $1 AND np.scope_id = $2 AND np.name LIKE 'app/%'`,
		scope.Kind, scope.ID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name, canon string
		if err := rows.Scan(&name, &canon); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if inPatchNames[name] {
			continue
		}
		files["/"+catalog.NamePath(name)] = canon
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return files, roots, nil
}

// typeErrorDiags maps error-severity tsgo diagnostics to Verdict diagnostics.
func typeErrorDiags(res tsx.CheckResult, lowered []loweredDef) []Diagnostic {
	pathToDef := map[string]loweredDef{}
	for _, ld := range lowered {
		pathToDef["/"+ld.Module+".ts"] = ld
	}
	var out []Diagnostic
	for _, d := range res.Diagnostics {
		if d.Category != "Error" {
			continue
		}
		dg := Diagnostic{
			StageOrVerifier: "tsgo",
			Code:            fmt.Sprintf("TS%d", d.Code),
			Severity:        "error",
			Loc:             Loc{Span: fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Col)},
			Message:         d.Message,
		}
		if ld, ok := pathToDef[d.File]; ok {
			dg.Subject = ld.Module
		}
		out = append(out, dg)
	}
	return out
}

// --- catalog reads/writes ----------------------------------------------------

func loadGrants(ctx context.Context, q catalog.Querier, subject string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT capability FROM grant_row WHERE subject = $1`, subject)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for rows.Next() {
		var cap string
		if err := rows.Scan(&cap); err != nil {
			rows.Close()
			return nil, err
		}
		out[cap] = true
	}
	return out, rows.Err()
}

func insertAdmission(ctx context.Context, q catalog.Querier, auth Principal, hashes []string) (int64, error) {
	var id int64
	_, err := q.QueryRow(ctx, `
INSERT INTO admission (actor_kind, actor_id, via, submitted_hashes, verifier_report)
VALUES ($1, $2, $3, $4::text[], '{}'::jsonb) RETURNING id`,
		[]any{auth.ActorKind, auth.ActorID, auth.Via, hashes}, &id)
	return id, err
}

func insertDefinitions(ctx context.Context, q catalog.Querier, lowered []loweredDef, admissionID int64, im *Image) error {
	verify := func(hash string, ast []byte) error {
		n, err := rast.Decode(ast)
		if err != nil {
			return fmt.Errorf("decode ast: %w", err)
		}
		if !rast.Verify(n, hash) {
			return fmt.Errorf("hash mismatch: stored bytes do not rehash to %s", hash)
		}
		return nil
	}
	for _, ld := range lowered {
		def := catalog.Def{
			Hash:          ld.Def.Hash,
			ASTSchemaVer:  rast.SchemaVersion,
			Kind:          catalogKind(ld.Def.Kind),
			AST:           rast.Encode(ld.Def.Body),
			CanonicalText: lower.CanonicalText(ld.Def),
			Contracts:     contractsMirror(ld.Def, im), // ADR-02 §3 mirror column
			Deps:          depHashes(ld.Def.Deps),
			AdmissionID:   admissionID,
		}
		if _, err := catalog.InsertDefinition(ctx, q, def, verify); err != nil {
			return err
		}
		meta := catalog.Meta{Hash: ld.Def.Hash, Docstring: ld.Def.Docstring}
		if _, err := catalog.InsertMeta(ctx, q, meta); err != nil {
			return err
		}
	}
	return nil
}

func depHashes(deps []rast.Dep) []string {
	// Distinct dependency EDGES can share a content hash (every std type shares
	// the opaque genesis body), so dedupe by hash: the deps column is the set of
	// referent hashes for the I2 FK check, not the edge list.
	seen := map[string]bool{}
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if seen[d.Hash] {
			continue
		}
		seen[d.Hash] = true
		out = append(out, d.Hash)
	}
	sort.Strings(out)
	return out
}

func isNoop(ctx context.Context, q catalog.Querier, lowered []loweredDef, scope Scope) (bool, error) {
	for _, ld := range lowered {
		var head string
		found, err := q.QueryRow(ctx,
			`SELECT hash FROM name_pointer WHERE name = $1 AND scope_kind = $2 AND scope_id = $3`,
			[]any{ld.CatalogName, scope.Kind, scope.ID}, &head)
		if err != nil {
			return false, err
		}
		if !found || head != ld.Def.Hash {
			return false, nil
		}
	}
	return true, nil
}

func recordReport(ctx context.Context, q catalog.Querier, admissionID int64, tsgoMs int64, stages []Stage, migrationSQL string, v Verdict) error {
	// The full Verdict is the verifier_report (ADR-07 §6: verdicts-as-rows); the
	// delta and seeders are ALSO written to their own columns for direct querying.
	report, err := json.Marshal(v)
	if err != nil {
		return err
	}
	deltaJSON, err := json.Marshal(v.Delta)
	if err != nil {
		return err
	}
	seeders := v.Seeders
	if seeders == nil {
		seeders = []Seeder{}
	}
	seedersJSON, err := json.Marshal(seeders)
	if err != nil {
		return err
	}
	var migArg any
	if migrationSQL != "" {
		migArg = migrationSQL
	}
	_, err = q.Exec(ctx,
		`UPDATE admission SET tsgo_ms = $1, verifier_report = $2::jsonb, migration_sql = $3,
		    verdict_delta = $4::jsonb, seeders = $5::jsonb WHERE id = $6`,
		tsgoMs, string(report), migArg, string(deltaJSON), string(seedersJSON), admissionID)
	return err
}

// --- refusals ----------------------------------------------------------------

func reject(ctx context.Context, conn *pgwire.Conn, auth Principal, patch Patch, v Verdict, diags []Diagnostic) (Verdict, bool, error) {
	v.Outcome = OutcomeRejected
	v.Diagnostics = diags
	if err := finishRefusal(ctx, conn, auth, patch, &v); err != nil {
		return Verdict{}, false, err
	}
	return v, false, nil
}

func staleBase(ctx context.Context, conn *pgwire.Conn, auth Principal, patch Patch, v Verdict, name string) (Verdict, bool, error) {
	v.Outcome = OutcomeStaleBase
	v.Diagnostics = []Diagnostic{{
		StageOrVerifier: "cas", Code: "STALE_BASE", Severity: "error", Subject: name,
		Message: "the pointer no longer names the declared base hash (a concurrent admission won, or the name already exists); re-read the head and resubmit",
		Fix:     "re-read the current head hash and resubmit with an updated base",
	}}
	if err := finishRefusal(ctx, conn, auth, patch, &v); err != nil {
		return Verdict{}, false, err
	}
	return v, false, nil
}

// finishRefusal rolls the admission transaction back (leaving no admission row —
// ADR-03 §5) and writes the durable gate_refusal ledger row on a separate
// autocommit statement (ADR-03 §1 table 6). The refusal_id is minted before the
// verdict returns (ADR-07 R1-08).
func finishRefusal(ctx context.Context, conn *pgwire.Conn, auth Principal, patch Patch, v *Verdict) error {
	if conn.InTx() {
		if err := conn.Rollback(ctx); err != nil {
			return err
		}
	}
	v.RefusalID = NewUUID()
	if !nonGreen(v.Outcome) {
		return nil
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	hashes := make([]string, 0, len(v.Hashes))
	for _, h := range v.Hashes {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	scopeAttempted := fmt.Sprintf("%d:%s", patch.TargetScope.Kind, patch.TargetScope.ID)
	_, err = conn.Exec(ctx, `
INSERT INTO gate_refusal (refusal_id, principal, scope_attempted, submitted_hashes, outcome, verdict)
VALUES ($1, $2, $3, $4::text[], $5, $6::jsonb)`,
		v.RefusalID, auth.Subject(), scopeAttempted, hashes, v.Outcome, string(blob))
	return err
}

// --- helpers -----------------------------------------------------------------

// codeDeadlock is Postgres deadlock_detected.
const codeDeadlock = "40P01"

// codeInFailedTxn is Postgres in_failed_sql_transaction: a serialization failure
// (40001) raised inside a multi-statement helper aborts the whole transaction, and
// any SUBSEQUENT statement on it returns 25P02, MASKING the original 40001. Under
// heavy concurrent DB load (BUILD-D's reactive fan-out adds this) that mask can be
// the error the pipeline sees, so it must retry it exactly as it would the
// underlying 40001 — the same fresh-snapshot retry, bounded by maxRetries.
const codeInFailedTxn = "25P02"

// isSerialization reports whether err is a concurrency conflict that aborts one
// admission txn cleanly and is safe to retry against a fresh snapshot. Beyond a
// bare 40001, two racing admissions to the SAME new name surface their conflict
// as a pointer-PK deadlock (40P01), a name_pointer unique violation (23505), or
// an I4 history-window exclusion violation (23P01) — all of which resolve
// deterministically to stale-base once the winner has committed and the loser
// retries against it (ADR-03 §5 CAS contract). 25P02 is a 40001 masked by a
// follow-on statement on the aborted txn (BUILD-D: surfaced under reactive load).
func isSerialization(err error) bool {
	return pgwire.IsCode(err, pgwire.CodeSerializationFailure) ||
		pgwire.IsCode(err, codeDeadlock) ||
		pgwire.IsCode(err, codeInFailedTxn) ||
		pgwire.IsCode(err, pgwire.CodeUniqueViolation) ||
		pgwire.IsCode(err, pgwire.CodeExclusionViolation)
}

func convertLowerDiags(ds []lower.Diagnostic, module string) []Diagnostic {
	out := make([]Diagnostic, 0, len(ds))
	for _, d := range ds {
		subject := d.Subject
		if subject == "" {
			subject = module
		}
		out = append(out, Diagnostic{
			StageOrVerifier: "lower",
			Code:            d.Code,
			Severity:        d.Severity,
			Subject:         subject,
			Loc:             Loc{Span: fmt.Sprintf("%s:%d:%d", module, d.Line, d.Col)},
			Message:         d.Message,
			Fix:             d.Fix,
		})
	}
	return out
}
