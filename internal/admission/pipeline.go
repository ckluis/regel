package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
	"regel.dev/regel/internal/tsx"
)

// maxRetries bounds SERIALIZABLE retries (ADR-07 §6): after this many
// serialization failures the pipeline returns retry-exhausted.
const maxRetries = 3

// loweredDef pairs a lowered definition with its full catalog name and module.
type loweredDef struct {
	CatalogName string
	Module      string
	Def         lower.Definition
}

// Admit runs the whole ADR-03 §5 / ADR-07 §1 admission pipeline (Stage-A subset)
// for one patch, over a dedicated connection, and returns the structured Verdict.
// A non-nil error is an internal fault (DB unavailable, encode failure) that the
// caller maps to HTTP 500 / a nonzero CLI exit; every ordinary refusal is a
// Verdict outcome, not an error. Identity and scope are bound from auth, never
// the patch body (§2a).
func Admit(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image) (Verdict, error) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		v, retry, err := admitOnce(ctx, conn, patch, auth, im)
		if err != nil {
			return Verdict{}, err
		}
		if retry {
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
func admitOnce(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image) (Verdict, bool, error) {
	base := time.Now().UTC().Format(time.RFC3339Nano)
	v := Verdict{Hashes: map[string]string{}, Epoch: im.Epoch, BaseSnapshot: base}
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
		if err := conn.Commit(ctx); err != nil {
			if isSerialization(err) {
				return Verdict{}, true, nil
			}
			return Verdict{}, false, err
		}
		committed = true
		mark("already-admitted", "pass")
		v.Outcome = OutcomeAlreadyAdmitted
		v.AdmissionID = admissionID
		v.Stages = stages
		v.Diagnostics = []Diagnostic{}
		return v, false, nil
	}

	// --- 3 INSERT definition / definition_meta (dep order, rast re-hash) -----
	if err := insertDefinitions(ctx, conn, lowered, admissionID); err != nil {
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
	res, cerr := tsx.Typecheck(tsx.CheckRequest{Files: files, RootFiles: roots})
	tsgoMs := time.Since(tcStart).Milliseconds()
	if cerr != nil {
		return Verdict{}, false, cerr
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
	// V2/V4/V5 are increment-C2 seam stubs that pass trivially (clearly marked).
	if vdiags := verifyV1(lowered, patch, grants, im); len(vdiags) > 0 {
		mark("V1", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V1", "pass")
	_ = verifyV2(lowered, plan, im)
	mark("V2", "pass")
	if vdiags := verifyV3(plan); len(vdiags) > 0 {
		mark("V3", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V3", "pass")
	_ = verifyV4(lowered, im)
	mark("V4", "pass")
	_ = verifyV5(lowered)
	mark("V5", "pass")
	if vdiags := verifyV6(plan); len(vdiags) > 0 {
		mark("V6", "fail")
		v.Stages = stages
		return reject(ctx, conn, auth, patch, v, vdiags)
	}
	mark("V6", "pass")

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

	// Persist the verifier report + applied migration_sql on the committed row.
	if err := recordReport(ctx, conn, admissionID, tsgoMs, stages, plan.MigrationSQL); err != nil {
		if isSerialization(err) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, err
	}

	if err := conn.Commit(ctx); err != nil {
		if isSerialization(err) {
			return Verdict{}, true, nil
		}
		return Verdict{}, false, err
	}
	committed = true

	v.Outcome = OutcomeAdmitted
	v.Admitted = true
	v.AdmissionID = admissionID
	v.Stages = stages
	v.Diagnostics = []Diagnostic{}
	return v, false, nil
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
// STAGE-A RESIDUE: L1 serves one file per definition-name at "/{name}.ts"
// (the name→path function). App→app imports that address a MODULE aggregate are
// a Stage-B concern; Stage-A patches import only std (served by L0), so unimported
// L1 files are inert under bundler resolution. L2 typechecks the submitted source
// rather than re-rendered canonical text.
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
		files["/"+name+".ts"] = canon
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

func insertDefinitions(ctx context.Context, q catalog.Querier, lowered []loweredDef, admissionID int64) error {
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
	out := make([]string, 0, len(deps))
	for _, d := range deps {
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

func recordReport(ctx context.Context, q catalog.Querier, admissionID int64, tsgoMs int64, stages []Stage, migrationSQL string) error {
	report, err := json.Marshal(map[string]any{"stages": stages})
	if err != nil {
		return err
	}
	var migArg any
	if migrationSQL != "" {
		migArg = migrationSQL
	}
	_, err = q.Exec(ctx,
		`UPDATE admission SET tsgo_ms = $1, verifier_report = $2::jsonb, migration_sql = $3 WHERE id = $4`,
		tsgoMs, string(report), migArg, admissionID)
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

// isSerialization reports whether err is a concurrency conflict that aborts one
// admission txn cleanly and is safe to retry against a fresh snapshot. Beyond a
// bare 40001, two racing admissions to the SAME new name surface their conflict
// as a pointer-PK deadlock (40P01), a name_pointer unique violation (23505), or
// an I4 history-window exclusion violation (23P01) — all of which resolve
// deterministically to stale-base once the winner has committed and the loser
// retries against it (ADR-03 §5 CAS contract).
func isSerialization(err error) bool {
	return pgwire.IsCode(err, pgwire.CodeSerializationFailure) ||
		pgwire.IsCode(err, codeDeadlock) ||
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
