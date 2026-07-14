package admission

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"regel.dev/regel/gate/redpath"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/mutants"
)

// harness_test.go is the ADR-07 §5 adversarial harness: the ONE runner that
// drives the whole hostile corpus (gate/redpath) through the REAL admission
// pipeline, the dual mutation-testing engine (direction (i) definition mutants +
// direction (ii) production-code mutant registry over verifiers + grammar gate +
// resolver), and the versioned coverage-as-data gate with its monotone-regression
// check. Red-path-first: a surviving direction-(ii) mutant fails the harness.

// harnessPrefix uniquifies module names across the many admissions one harness
// run drives over a SINGLE scratch DB, so re-running the same fixture under a
// different mutant never collides on an already-admitted name (each admission is
// a fresh catalog name). Tests run sequentially, so a plain counter is safe.
var harnessPrefix int

func nextPrefix() string { harnessPrefix++; return fmt.Sprintf("h%d", harnessPrefix) }

// seedHarnessGrants grants engineer:dev the capabilities the corpus's declared
// (but not necessarily granted) fixtures need to reach the verifier under test.
func seedHarnessGrants(t *testing.T, w *world, ctx context.Context) {
	t.Helper()
	for _, cap := range []string{"crm.read", "mail.send"} {
		if _, err := w.conn.Exec(ctx,
			`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,$2,'','harness')`,
			engineer("dev").Subject(), cap); err != nil {
			t.Fatal(err)
		}
	}
}

// runFixture translates one redpath.Fixture into a real Patch + Principal (under a
// fresh module prefix), admits its prelude if any, and drives the hostile source
// through the REAL pipeline, returning the Verdict. It asserts nothing — callers
// decide whether the verdict should be red (corpus/baseline) or green (a mutant
// kill / a direction-(i) clean twin).
func runFixture(t *testing.T, w *world, ctx context.Context, fx redpath.Fixture) Verdict {
	t.Helper()
	prefix := nextPrefix()
	mod := prefix + "/" + fx.Module

	auth := engineer("dev")
	if fx.Agent {
		auth = Principal{ActorKind: "agent", ActorID: "a1", Via: "mcp", Chain: catalog.Chain{OrgID: fx.OrgID}}
	}

	baseHashes := map[string]string{}
	if fx.Prelude != "" {
		pp := Patch{
			Modules:     []ModuleSrc{{ModuleName: mod, Source: fx.Prelude}},
			TargetScope: Scope{Kind: 0, ID: ""},
			BaseHashes:  map[string]string{},
		}
		pv, err := Admit(ctx, w.conn, pp, auth, BuildImage())
		if err != nil {
			t.Fatalf("%s prelude admit: %v", fx.Name, err)
		}
		if pv.Outcome != OutcomeAdmitted {
			t.Fatalf("%s prelude must admit, got %q (%+v)", fx.Name, pv.Outcome, pv.Diagnostics)
		}
		if fx.BaseName != "" {
			var h string
			ok, err := w.conn.QueryRow(ctx,
				`SELECT hash FROM name_pointer WHERE name=$1 AND scope_kind=0 AND scope_id=''`,
				[]any{mod + "/" + fx.BaseName}, &h)
			if err != nil || !ok {
				t.Fatalf("%s prelude head: ok=%v err=%v", fx.Name, ok, err)
			}
			baseHashes[mod+"/"+fx.BaseName] = h
		}
	}

	p := Patch{
		Modules:     []ModuleSrc{{ModuleName: mod, Source: fx.Source}},
		TargetScope: Scope{Kind: 0, ID: ""},
		BaseHashes:  baseHashes,
	}
	if len(fx.Declared) > 0 {
		p.DeclaredCapabilities = map[string][]string{}
		for def, caps := range fx.Declared {
			p.DeclaredCapabilities[mod+"/"+def] = caps
		}
	}
	if len(fx.Tier) > 0 {
		p.Tier = map[string]string{}
		for def, tr := range fx.Tier {
			p.Tier[mod+"/"+def] = tr
		}
	}
	if fx.Intent != "" {
		p.Intent = fx.Intent
	}
	for _, s := range fx.ReadLog {
		p.ReadLog = append(p.ReadLog, ReadLogEntry{
			SourceKind: s.SourceKind, SourceRef: s.SourceRef,
			Scope: Scope{Kind: s.ScopeKind, ID: s.ScopeID}, SeededBy: s.SeededBy,
		})
	}

	v, err := Admit(ctx, w.conn, p, auth, BuildImage())
	if err != nil {
		t.Fatalf("%s admit: %v", fx.Name, err)
	}
	return v
}

func hasCode(v Verdict, code string) bool {
	for _, d := range v.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func isGreen(v Verdict) bool {
	return v.Outcome == OutcomeAdmitted || v.Outcome == OutcomeAlreadyAdmitted
}

// TestAdversarialHarness is the ADR-07 §5 release-gate machinery over one scratch
// DB. Subtests share the DB (unique module prefixes keep admissions collision-
// free) so the whole harness stays fast enough for `go test ./...`.
func TestAdversarialHarness(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	seedHarnessGrants(t, w, ctx)

	// --- the hostile corpus is genuinely hostile: every fixture rejects with its
	//     SPECIFIC code (a green result on a hostile fixture fails the run). ------
	t.Run("corpus-hostile-baseline", func(t *testing.T) {
		if len(redpath.Corpus) == 0 {
			t.Fatal("empty corpus")
		}
		for _, fx := range redpath.Corpus {
			v := runFixture(t, w, ctx, fx)
			if v.Outcome != OutcomeRejected {
				t.Errorf("%s [%s]: outcome=%q, want rejected — a green hostile fixture fails the run; diags=%+v",
					fx.Name, fx.Component, v.Outcome, v.Diagnostics)
				continue
			}
			if !hasCode(v, fx.ExpectCode) {
				t.Errorf("%s [%s]: want reject code %q, got %+v", fx.Name, fx.Component, fx.ExpectCode, v.Diagnostics)
			}
		}
	})

	// --- direction (ii): a mutant registry compiled into the production
	//     enforcement code (verifiers + grammar gate + resolver). For each mutant,
	//     arm it, run the hostile corpus, and assert AT LEAST ONE fixture flips
	//     green (the corpus KILLS the mutant). A survivor blocks the release. -----
	killers := map[string][]string{}
	t.Run("direction-ii-production-mutants", func(t *testing.T) {
		mutants.Arm()
		defer mutants.Disarm()
		for _, m := range mutants.All {
			mutants.Enable(m.Name)
			var got []string
			for _, fx := range redpath.Corpus {
				v := runFixture(t, w, ctx, fx)
				if isGreen(v) {
					got = append(got, fx.Name) // the corpus detected the weakening
				}
			}
			mutants.Disable(m.Name)
			killers[m.Name] = got
			if len(got) == 0 {
				t.Errorf("SURVIVING MUTANT %s (%s / %s): no hostile fixture went green — "+
					"a coverage hole that blocks the release", m.Name, m.Component, m.Weakens)
			}
		}
	})

	// --- direction (i): mutate admitted-clean DEFINITIONS and assert the OWNING
	//     verifier rejects each mutant with its code. Each row admits the clean
	//     twin (proving the mutation is what flips it) then rejects the mutant. ----
	t.Run("direction-i-definition-mutants", func(t *testing.T) {
		for _, dm := range directionOneMutations() {
			cv := runFixture(t, w, ctx, dm.clean)
			if cv.Outcome != OutcomeAdmitted {
				t.Errorf("%s clean twin must admit, got %q (%+v)", dm.name, cv.Outcome, cv.Diagnostics)
			}
			mv := runFixture(t, w, ctx, dm.mutant)
			if mv.Outcome != OutcomeRejected {
				t.Errorf("%s mutant must be rejected by %s, got %q (%+v)", dm.name, dm.mutant.Component, mv.Outcome, mv.Diagnostics)
				continue
			}
			if !hasCode(mv, dm.mutant.ExpectCode) {
				t.Errorf("%s mutant: want %s from %s, got %+v", dm.name, dm.mutant.ExpectCode, dm.mutant.Component, mv.Diagnostics)
			}
			if len(mv.Diagnostics) > 0 && mv.Diagnostics[0].StageOrVerifier != dm.owner {
				t.Errorf("%s mutant: want owner %q, got %q", dm.name, dm.owner, mv.Diagnostics[0].StageOrVerifier)
			}
		}
	})

	// --- coverage as data + monotone-regression gate (ADR-07 §5). -------------
	t.Run("coverage-and-monotone-gate", func(t *testing.T) {
		cur := computeCoverage(killers)
		if len(cur) != 8 {
			t.Fatalf("coverage must cover 8 components, got %d", len(cur))
		}
		// Every direction-(ii) mutant is killed in the green state, so every
		// component's mutation_score is 1.0.
		for _, c := range cur {
			if c.score < 1.0 {
				t.Errorf("component %s mutation_score = %.3f < 1.0 (a surviving mutant)", c.component, c.score)
			}
		}
		writeCoverage(t, w, ctx, 1, cur)
		if got := w.count("SELECT count(*) FROM verifier_coverage WHERE epoch=1"); got != 8 {
			t.Fatalf("verifier_coverage epoch-1 rows = %d, want 8", got)
		}

		// Seed a non-regressing predecessor epoch (0) equal to the current epoch,
		// then assert the monotone gate ADMITS the current epoch.
		writeCoverage(t, w, ctx, 0, cur)
		if err := assertMonotone(ctx, w.conn, 1, cur); err != nil {
			t.Fatalf("monotone gate must admit a non-regressing epoch: %v", err)
		}

		// Demonstrably REFUSE a regression: make the epoch-0 predecessor for V6
		// carry an extra threat class the current epoch lacks — the current epoch
		// now SHRINKS the inventory, which the gate must refuse.
		if _, err := w.conn.Exec(ctx,
			`UPDATE verifier_coverage SET threat_class_ids = array_append(threat_class_ids,'ddl.rewrite')
			 WHERE epoch=0 AND component='V6'`); err != nil {
			t.Fatal(err)
		}
		if err := assertMonotone(ctx, w.conn, 1, cur); err == nil {
			t.Fatal("monotone gate must REFUSE a shrunk threat inventory (V6 dropped ddl.rewrite)")
		}

		// Demonstrably REFUSE a mutation_score regression too.
		if _, err := w.conn.Exec(ctx,
			`UPDATE verifier_coverage SET threat_class_ids = array_remove(threat_class_ids,'ddl.rewrite'),
			     mutation_score = 1.0 WHERE epoch=0 AND component='V6'`); err != nil {
			t.Fatal(err)
		}
		if _, err := w.conn.Exec(ctx,
			`UPDATE verifier_coverage SET mutation_score = 0.5 WHERE epoch=1 AND component='V1'`); err != nil {
			t.Fatal(err)
		}
		regressed := make([]covRow, len(cur))
		copy(regressed, cur)
		for i := range regressed {
			if regressed[i].component == "V1" {
				regressed[i].score = 0.5
			}
		}
		if err := assertMonotone(ctx, w.conn, 1, regressed); err == nil {
			t.Fatal("monotone gate must REFUSE a mutation_score regression (V1 1.0→0.5)")
		}
	})
}

// --- coverage-as-data ---------------------------------------------------------

// covRow is one verifier_coverage row (ADR-07 §5): the enforcement site's stated,
// queryable threat inventory + corpus size + mutation score for an epoch.
type covRow struct {
	component string
	threats   []string
	corpus    int
	score     float64
}

// coverageComponents is the closed set of enforcement SITES that carry a coverage
// row (R1-10: the six suite verifiers PLUS grammar-gate and resolver).
var coverageComponents = []string{"V1", "V2", "V3", "V4", "V5", "V6", "grammar-gate", "resolver"}

// computeCoverage projects the corpus + mutant-kill results into one coverage row
// per component: its stable threat inventory (from the corpus), corpus case count,
// and mutation score (owned mutants killed / owned mutants).
func computeCoverage(killers map[string][]string) []covRow {
	out := make([]covRow, 0, len(coverageComponents))
	for _, comp := range coverageComponents {
		threatSet := map[string]bool{}
		corpus := 0
		for _, fx := range redpath.Corpus {
			if fx.Component == comp {
				threatSet[fx.ThreatClass] = true
				corpus++
			}
		}
		threats := make([]string, 0, len(threatSet))
		for tc := range threatSet {
			threats = append(threats, tc)
		}
		sort.Strings(threats)

		owned, killed := 0, 0
		for _, m := range mutants.All {
			if m.Component != comp {
				continue
			}
			owned++
			if len(killers[m.Name]) > 0 {
				killed++
			}
		}
		score := 0.0
		if owned > 0 {
			score = float64(killed) / float64(owned)
		}
		out = append(out, covRow{component: comp, threats: threats, corpus: corpus, score: score})
	}
	return out
}

func writeCoverage(t *testing.T, w *world, ctx context.Context, epoch int, rows []covRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := w.conn.Exec(ctx, `
INSERT INTO verifier_coverage (epoch, component, threat_class_ids, corpus_case_count, mutation_score)
VALUES ($1,$2,$3::text[],$4,$5)
ON CONFLICT (epoch, component) DO UPDATE
  SET threat_class_ids=EXCLUDED.threat_class_ids, corpus_case_count=EXCLUDED.corpus_case_count,
      mutation_score=EXCLUDED.mutation_score`,
			epoch, r.component, r.threats, r.corpus, r.score); err != nil {
			t.Fatalf("write coverage %s: %v", r.component, err)
		}
	}
}

// assertMonotone is the coverage-monotonicity gate (ADR-07 §5): an epoch may not
// SHRINK any component's threat inventory nor REGRESS its mutation_score vs the
// nearest recorded prior epoch. A regression fails the release.
func assertMonotone(ctx context.Context, q catalog.Querier, epoch int, cur []covRow) error {
	for _, c := range cur {
		var pepoch int
		var pthreats []string
		var pscore float64
		ok, err := q.QueryRow(ctx, `
SELECT epoch, threat_class_ids, mutation_score FROM verifier_coverage
WHERE component=$1 AND epoch < $2 ORDER BY epoch DESC LIMIT 1`,
			[]any{c.component, epoch}, &pepoch, &pthreats, &pscore)
		if err != nil {
			return err
		}
		if !ok {
			continue // no predecessor — nothing to regress against
		}
		for _, tc := range pthreats {
			if !contains(c.threats, tc) {
				return fmt.Errorf("MONOTONE VIOLATION: component %s dropped threat class %q (epoch %d→%d)",
					c.component, tc, pepoch, epoch)
			}
		}
		if c.score+1e-9 < pscore {
			return fmt.Errorf("MONOTONE VIOLATION: component %s mutation_score regressed %.4f→%.4f (epoch %d→%d)",
				c.component, pscore, c.score, pepoch, epoch)
		}
	}
	return nil
}

// --- direction (i) definition mutations ---------------------------------------

type defMutation struct {
	name   string
	owner  string // stage_or_verifier the mutant diagnostic must carry
	clean  redpath.Fixture
	mutant redpath.Fixture
}

// directionOneMutations is the table of admitted-clean definitions mutated one
// way each, with the OWNING verifier that must reject the mutant (ADR-07 §5
// direction (i)). Six mutations: drop a mask() (V2), widen a declared grant (V1),
// capture a handle across await (V5), unwire a policy path (V3), make a contract
// clause effectful (V4), turn a field-add into a field-drop (V6).
func directionOneMutations() []defMutation {
	corpus := map[string]redpath.Fixture{}
	for _, fx := range redpath.Corpus {
		corpus[fx.Name] = fx
	}
	return []defMutation{
		{
			name: "drop-mask", owner: "V2",
			clean: redpath.Fixture{
				Name: "d1-v2-clean", Component: "V2", Module: "app/d1",
				Source: `import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function showOwner(owner: Vault<string>): string {
  return mask(owner);
}
`},
			mutant: corpus["v2-pii-escape-return"],
		},
		{
			name: "widen-declared-grant", owner: "V1",
			clean: redpath.Fixture{
				Name: "d2-v1-clean", Component: "V1", Module: "app/d2",
				Declared: map[string][]string{"notify": {"mail.send"}},
				Source: `import { send } from "std/mail";
export function notify(): void {
  send("a@b.com", "hi");
}
`},
			mutant: corpus["v1-cap-ungranted"],
		},
		{
			name: "capture-across-await", owner: "V5",
			clean: redpath.Fixture{
				Name: "d3-v5-clean", Component: "V5", Module: "app/d3",
				Tier: map[string]string{"wf": "workflow"},
				Source: `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(): Promise<Conn> {
  await sleep(1);
  const c: Conn = connect();
  return c;
}
`},
			mutant: corpus["v5-capture-unserializable"],
		},
		{
			name: "unwire-policy", owner: "V3",
			clean: redpath.Fixture{
				Name: "d4-v3-clean", Component: "V3", Module: "app/d4",
				Source: `import { resource } from "std/resource";
import { policy } from "std/policy";
export const teamScoped = policy("team");
export const Deal = resource({
  fields: { title: "text" },
  policy: teamScoped,
});
`},
			mutant: corpus["v3-policy-unwired"],
		},
		{
			name: "effectful-contract", owner: "V4",
			clean: redpath.Fixture{
				Name: "d5-v4-clean", Component: "V4", Module: "app/d5",
				Source: `import { pre, post } from "std/contract";
export function transfer(amount: number): number {
  pre(amount > 0);
  post(amount > 0);
  return amount;
}
`},
			mutant: corpus["v4-contract-effectful"],
		},
		{
			name: "field-add-to-drop", owner: "V6",
			clean: redpath.Fixture{
				Name: "d6-v6-clean", Component: "V6", Module: "app/d6", BaseName: "Deal",
				Prelude: `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { title: "text" },
  policy: orgScoped,
});
`,
				Source: `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { title: "text", owner: "pii:text" },
  policy: orgScoped,
});
`},
			mutant: corpus["v6-ddl-destructive"],
		},
	}
}
