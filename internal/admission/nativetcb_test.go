package admission

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"regel.dev/regel/gate/nativetcb"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/oracle"
	"regel.dev/regel/internal/rast"
)

// nativetcb_test.go is the ADR-10 §8 native-TCB adversarial harness — co-equal
// with ADR-07 §5's gate/redpath, but aimed one ring lower at the native-Go std
// bodies that hold the real authority. It seeds the three threat classes
// (gate/nativetcb) and proves the surrounding machinery catches each, or records
// exactly what the TCB is trusted for. Release-blocking: a seeded malicious native
// the machinery fails to catch, or a caught-only-by-documentation case with no
// trusted-for row, turns this test RED (the same standing as the hostile corpus).
// Its coverage is carried as verifier_coverage-style MONOTONE rows keyed on the
// three classes; a class silently dropped is a release blocker.

// seedNativeTCBGrants grants engineer:dev the capabilities the vault-leak caller
// fixtures declare, so those admissions reach V2 (the verifier under test) rather
// than failing earlier at the capability grant gate.
func seedNativeTCBGrants(t *testing.T, w *world, ctx context.Context) {
	t.Helper()
	for _, cap := range []string{"mail.send", "http.post"} {
		if _, err := w.conn.Exec(ctx,
			`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,$2,'','nativetcb')`,
			engineer("dev").Subject(), cap); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureNativeHash computes the content-address of a fixture NativeBody the SAME
// way the genesis image does (rast.Normalize + rast.Address over a KNativeBody
// carrying the intrinsic symbol). A fixture intrinsic is never a roster name, so
// its hash is absent from the shipped image (the purity subtest asserts it).
func fixtureNativeHash(intrinsic string) (string, *rast.Node) {
	nb := rast.Normalize(&rast.Node{Kind: rast.KNativeBody, Str: intrinsic,
		Kids: []*rast.Node{{Kind: rast.TKeyword, Str: "unknown"}}})
	return rast.Address(nb), nb
}

// imageResolver builds a lowering resolver over the genesis image's std world,
// plus optional extra (intrinsic → hash) overrides for fixture natives.
func imageResolver(im *Image, extra map[string]string) lower.Resolver {
	byName := map[string]string{}
	for _, e := range im.Entries {
		byName[e.CatalogName] = e.Hash
	}
	return func(qualified string) (string, bool) {
		if h, ok := extra[qualified]; ok {
			return h, true
		}
		// "std/mail.send" → "std/mail/send" (the lowering resolver contract).
		name := qualified
		if i := strings.LastIndex(qualified, "."); i >= 0 {
			name = qualified[:i] + "/" + qualified[i+1:]
		}
		h, ok := byName[name]
		return h, ok
	}
}

// TestNativeTCBHarness is the ADR-10 §8 release-gate machinery over one scratch DB.
func TestNativeTCBHarness(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	im := w.im
	seedNativeTCBGrants(t, w, ctx)

	// === vault-leak class ====================================================

	// Leg 1 (admission): a native cannot become a laundering path around masking.
	// An admitted CALLER cannot route a Vault value into a capability-bearing
	// egress sink unmasked — V2 refuses the caller, so the evil native never runs.
	t.Run("vault-leak/admission", func(t *testing.T) {
		for _, fx := range nativetcb.VaultLeakFixtures {
			p := Patch{
				Modules:     []ModuleSrc{{ModuleName: fx.Module, Source: fx.Source}},
				TargetScope: Scope{Kind: 0, ID: ""},
				BaseHashes:  map[string]string{},
			}
			if len(fx.Declared) > 0 {
				p.DeclaredCapabilities = map[string][]string{}
				for def, caps := range fx.Declared {
					p.DeclaredCapabilities[fx.Module+"/"+def] = caps
				}
			}
			v, err := Admit(ctx, w.conn, p, engineer("dev"), im)
			if err != nil {
				t.Fatalf("%s admit: %v", fx.Name, err)
			}
			if v.Outcome != OutcomeRejected {
				t.Errorf("%s: outcome=%q, want rejected — a native laundering an unmasked Vault must be caught; diags=%+v",
					fx.Name, v.Outcome, v.Diagnostics)
				continue
			}
			if !hasCode(v, fx.ExpectCode) {
				t.Errorf("%s: want reject code %q, got %+v", fx.Name, fx.ExpectCode, v.Diagnostics)
			}
		}
	})

	// Leg 2 (storage): the vault storage layer never hands out plaintext without a
	// reveal grant. A pii value is sealed ciphertext-only; the default read path
	// yields the mask token; only the explicit (grant-gated) reveal path decrypts;
	// after crypto-shred the ciphertext is permanently undecryptable.
	t.Run("vault-leak/storage", func(t *testing.T) {
		const res, subj, field, secret = "Deal", "row-1", "owner", "Ada Lovelace"
		if err := VaultPut(ctx, w.conn, res, subj, field, secret); err != nil {
			t.Fatal(err)
		}
		// The plaintext lives ONLY in the vault ciphertext, never in cleartext.
		var ct string
		ok, err := w.conn.QueryRow(ctx,
			`SELECT ciphertext FROM vault WHERE resource=$1 AND subject_id=$2 AND field=$3`,
			[]any{res, subj, field}, &ct)
		if err != nil || !ok {
			t.Fatalf("vault ciphertext row: ok=%v err=%v", ok, err)
		}
		if strings.Contains(ct, secret) {
			t.Fatalf("ciphertext leaks plaintext: %q", ct)
		}
		// The grant-gated reveal path is the ONLY way to plaintext.
		pt, revealed, err := VaultReveal(ctx, w.conn, res, subj, field)
		if err != nil {
			t.Fatal(err)
		}
		if !revealed || pt != secret {
			t.Fatalf("reveal path: revealed=%v pt=%q, want the plaintext under grant", revealed, pt)
		}
		// Crypto-shred deletes the key; the ciphertext becomes undecryptable and
		// reads collapse to the mask token — no plaintext survives.
		if _, _, err := CryptoShred(ctx, w.conn, res, subj, "nativetcb"); err != nil {
			t.Fatal(err)
		}
		pt2, revealed2, err := VaultReveal(ctx, w.conn, res, subj, field)
		if err != nil {
			t.Fatal(err)
		}
		if revealed2 || pt2 != VaultMaskToken {
			t.Fatalf("post-shred reveal: revealed=%v pt=%q, want (false, mask token)", revealed2, pt2)
		}
	})

	// Leg 3 (counterfactual): the evil egress native DOES leak a value handed to
	// it — the authority the trusted-for statement records, and the reason V2 must
	// bound the caller (legs 1+2). Run directly, off any masking path.
	t.Run("vault-leak/counterfactual-authority", func(t *testing.T) {
		const intrinsic = "std/evilexfil.sink"
		h, nb := fixtureNativeHash(intrinsic)
		reg := im.Registry()
		reg.Register(h, nativetcb.EvilExfilSink)
		reg.SetEffectClass(h, "external") // declares its egress honestly; it holds the authority
		src := cek.MapSource{}
		for _, e := range im.Entries {
			src[e.Hash] = e.Body
		}
		src[h] = nb
		resolve := imageResolver(im, map[string]string{intrinsic: h})
		res := lower.Module(`import { sink } from "std/evilexfil";
export function f(): void {
  sink("Ada Lovelace");
}
`, lower.ModuleContext{ModuleName: "app/exfil", Resolve: resolve})
		if !res.OK() {
			t.Fatalf("exfil caller does not lower: %+v", res.Diagnostics)
		}
		var entry string
		for _, d := range res.Definitions {
			src[d.Hash] = d.Body
			if d.Name == "f" {
				entry = d.Hash
			}
		}
		o := cek.New(src, reg).Run(ctx, cek.RunReq{
			DefHash: entry, Tier: cek.TierTrusted,
			Principal: cek.Principal{Subject: "tcb", IsOperator: true},
		})
		if o.Kind != cek.OutDone {
			t.Fatalf("exfil run: kind=%d err=%v", o.Kind, o.Err)
		}
		leaked := false
		for _, e := range o.Effects {
			if e.Class == "exfil" && e.Payload["leaked"] == "Ada Lovelace" {
				leaked = true
			}
		}
		if !leaked {
			t.Fatalf("evil exfil did not leak the value it holds authority over: %+v — "+
				"a native holding egress authority WILL leak a value it receives; the control is V2 bounding the caller", o.Effects)
		}
	})

	// === effect-order class ==================================================

	// A read-declared native that records a write/external effect is a lie about
	// its effect class. The §6 std-conformance gate in performNative catches it and
	// fails closed. RED-first: with the gate DISABLED (mutant armed) the evil
	// native goes UNCAUGHT — the effect is silently recorded — proving the gate is
	// what does the catching.
	t.Run("effect-order/conformance-catch", func(t *testing.T) {
		for _, fx := range nativetcb.EffectFixtures {
			h, nb := fixtureNativeHash(fx.Intrinsic)
			run := func() cek.Outcome {
				reg := im.Registry()
				reg.Register(h, fx.Native)
				reg.SetEffectClass(h, fx.DeclClass)
				src := cek.MapSource{}
				for _, e := range im.Entries {
					src[e.Hash] = e.Body
				}
				src[h] = nb
				resolve := imageResolver(im, map[string]string{fx.Intrinsic: h})
				res := lower.Module(fx.CallerSrc, lower.ModuleContext{ModuleName: fx.CallerMod, Resolve: resolve})
				if !res.OK() {
					t.Fatalf("%s caller does not lower: %+v", fx.Name, res.Diagnostics)
				}
				var entry string
				for _, d := range res.Definitions {
					src[d.Hash] = d.Body
					if d.Name == fx.Entry {
						entry = d.Hash
					}
				}
				return cek.New(src, reg).Run(ctx, cek.RunReq{
					DefHash: entry, Tier: cek.TierTrusted,
					Principal: cek.Principal{Subject: "tcb", IsOperator: true},
				})
			}

			// GREEN (gate live): caught, failed closed.
			o := run()
			if o.Kind != cek.OutError || o.Err == nil {
				t.Errorf("%s: kind=%d err=%v, want OutError (conformance catch, fail closed)", fx.Name, o.Kind, o.Err)
			} else if msg := o.Err.Error(); !strings.Contains(msg, "conformance") || !strings.Contains(msg, "effect-class") {
				t.Errorf("%s: err=%q, want a conformance effect-class violation", fx.Name, msg)
			}

			// RED-first (gate disabled): the seeded evil native goes UNCAUGHT.
			mutants.Arm()
			mutants.Enable("TCB_SKIP_EFFECT_CONFORMANCE")
			red := run()
			mutants.Disable("TCB_SKIP_EFFECT_CONFORMANCE")
			mutants.Disarm()
			if red.Kind != cek.OutDone {
				t.Errorf("%s RED state: kind=%d, want OutDone (uncaught) with the gate disabled", fx.Name, red.Kind)
			}
			leaked := false
			for _, e := range red.Effects {
				if e.Class == fx.WantEffect {
					leaked = true
				}
			}
			if !leaked {
				t.Errorf("%s RED state: the disabled-gate run recorded no %q effect — the control did not distinguish RED from GREEN", fx.Name, fx.WantEffect)
			}
		}
	})

	// === contract-violation class ============================================

	// A native whose runtime behavior diverges from its declared contract/signature
	// turns the ADR-04 §6 differential oracle RED: the production machine (running
	// the evil body) disagrees with the independent reference reducer (which runs
	// the honest semantics). Honest baseline agrees (zero divergences); the evil
	// body diverges on at least one vector — the difference IS the catch.
	t.Run("contract-violation/oracle-diverges", func(t *testing.T) {
		for _, fx := range nativetcb.ContractViolFixtures {
			oc, ok := oracleCaseByName(fx.OracleCase)
			if !ok {
				t.Fatalf("%s: oracle case %q not found", fx.Name, fx.OracleCase)
			}
			overrideHash := ""
			intrinsics := map[string]string{}
			for _, e := range im.Entries {
				intrinsics[e.Hash] = e.Intrinsic
				if e.Intrinsic == fx.OverrideIntr {
					overrideHash = e.Hash
				}
			}
			if overrideHash == "" {
				t.Fatalf("%s: override intrinsic %q not in image", fx.Name, fx.OverrideIntr)
			}

			diverge := func(evil bool) int {
				resolve := imageResolver(im, nil)
				res := lower.Module(oc.Source, lower.ModuleContext{ModuleName: oc.Module, Resolve: resolve})
				if !res.OK() {
					t.Fatalf("%s case does not lower: %+v", fx.Name, res.Diagnostics)
				}
				defs := map[string]*rast.Node{}
				var entry string
				for _, d := range res.Definitions {
					defs[d.Hash] = d.Body
					if d.Name == oc.Entry {
						entry = d.Hash
					}
				}
				reg := im.Registry()
				if evil {
					reg.Register(overrideHash, fx.Native) // override only the LOCAL registry
				}
				src := cek.MapSource{}
				for _, e := range im.Entries {
					src[e.Hash] = e.Body
				}
				for h, b := range defs {
					src[h] = b
				}
				interp := cek.New(src, reg)
				ref := &oracle.Machine{Defs: defs, Intrinsics: intrinsics}

				n := 0
				for _, input := range oc.Inputs {
					out := interp.Run(ctx, cek.RunReq{
						DefHash: entry, Args: cekArgs(input), Tier: cek.TierTrusted,
						Principal: cek.Principal{Subject: "oracle", IsOperator: true},
					})
					r := ref.Run(entry, oracleArgs(input))
					if cekVerdict(out) != refVerdict(r) {
						n++
					}
				}
				return n
			}

			if d := diverge(false); d != 0 {
				t.Errorf("%s honest baseline: %d divergence(s), want 0 (attribution impossible otherwise)", fx.Name, d)
			}
			if d := diverge(true); d == 0 {
				t.Errorf("%s evil body: 0 divergences — the differential oracle did not catch the contract-violating native", fx.Name)
			}
		}
	})

	// === honesty gates =======================================================

	// Every roster native holding real authority (a capability OR a declared effect
	// class) MUST be classified in the authority inventory, and every irreducible
	// (CaughtBy == "") entry MUST carry a trusted-for statement — the TCB is stated
	// as data, never a silent pass.
	t.Run("authority-inventory-complete", func(t *testing.T) {
		classified := map[string]nativetcb.Disposition{}
		for _, d := range nativetcb.AuthorityInventory {
			classified[d.Native] = d
			if d.CaughtBy == "" && d.TrustedFor == "" {
				t.Errorf("authority %q: neither caught-by nor trusted-for — a silent pass (ADR-10 §8 forbids it)", d.Native)
			}
		}
		for _, e := range im.Entries {
			if e.Capability == "" && e.EffectClass == "" {
				continue
			}
			if _, ok := classified[e.Intrinsic]; !ok {
				t.Errorf("roster native %q holds authority (cap=%q class=%q) but is unclassified in the authority inventory",
					e.Intrinsic, e.Capability, e.EffectClass)
			}
		}
	})

	// The seeded evil natives are registerable ONLY under test — the genesis image
	// contains no fixture hash. (The contract-violation fixtures override a genesis
	// body inside a LOCAL registry only; the process image is untouched, proven by
	// the honest baseline above staying green.)
	t.Run("shipped-image-purity", func(t *testing.T) {
		for _, fx := range nativetcb.EffectFixtures {
			h, _ := fixtureNativeHash(fx.Intrinsic)
			if _, present := im.ByHash[h]; present {
				t.Errorf("%s: fixture native hash %s is in the shipped genesis image — evil natives must be test-only", fx.Name, h)
			}
		}
		exfilHash, _ := fixtureNativeHash("std/evilexfil.sink")
		if _, present := im.ByHash[exfilHash]; present {
			t.Errorf("exfil fixture hash %s is in the shipped genesis image", exfilHash)
		}
	})

	// === monotone coverage gate =============================================

	t.Run("coverage-and-monotone-gate", func(t *testing.T) {
		cur := computeTCBCoverage()
		if len(cur) != 3 {
			t.Fatalf("native-TCB coverage must cover 3 threat classes, got %d", len(cur))
		}
		writeTCBCoverage(t, w, ctx, 1, cur)
		if got := w.count("SELECT count(*) FROM native_tcb_coverage WHERE epoch=1"); got != 3 {
			t.Fatalf("native_tcb_coverage epoch-1 rows = %d, want 3", got)
		}

		// A non-regressing predecessor epoch (0) equal to the current epoch: the
		// monotone gate ADMITS it.
		writeTCBCoverage(t, w, ctx, 0, cur)
		if err := assertTCBMonotone(ctx, w.conn, 1, cur); err != nil {
			t.Fatalf("monotone gate must admit a non-regressing epoch: %v", err)
		}

		// REFUSE a dropped class: seed an epoch-0 class the current epoch lacks.
		if _, err := w.conn.Exec(ctx,
			`INSERT INTO native_tcb_coverage (epoch, threat_class, fixture_ids, caught_by, trusted_for)
			 VALUES (0,'legacy-class', ARRAY['legacy-fx']::text[], 'legacy-control', ARRAY[]::text[])`); err != nil {
			t.Fatal(err)
		}
		if err := assertTCBMonotone(ctx, w.conn, 1, cur); err == nil {
			t.Fatal("monotone gate must REFUSE a dropped threat class (legacy-class present in epoch 0, absent in 1)")
		}
		if _, err := w.conn.Exec(ctx, `DELETE FROM native_tcb_coverage WHERE threat_class='legacy-class'`); err != nil {
			t.Fatal(err)
		}

		// REFUSE a shrunk fixture inventory: give epoch-0 vault-leak an extra
		// fixture the current epoch does not carry.
		if _, err := w.conn.Exec(ctx,
			`UPDATE native_tcb_coverage SET fixture_ids = array_append(fixture_ids,'dropped-fixture')
			 WHERE epoch=0 AND threat_class=$1`, nativetcb.ClassVaultLeak); err != nil {
			t.Fatal(err)
		}
		if err := assertTCBMonotone(ctx, w.conn, 1, cur); err == nil {
			t.Fatal("monotone gate must REFUSE a shrunk fixture inventory (vault-leak dropped a fixture)")
		}
		if _, err := w.conn.Exec(ctx,
			`UPDATE native_tcb_coverage SET fixture_ids = array_remove(fixture_ids,'dropped-fixture')
			 WHERE epoch=0 AND threat_class=$1`, nativetcb.ClassVaultLeak); err != nil {
			t.Fatal(err)
		}

		// REFUSE a dropped trusted-for statement.
		if _, err := w.conn.Exec(ctx,
			`UPDATE native_tcb_coverage SET trusted_for = array_append(trusted_for,'dropped-trust')
			 WHERE epoch=0 AND threat_class=$1`, nativetcb.ClassContractViol); err != nil {
			t.Fatal(err)
		}
		if err := assertTCBMonotone(ctx, w.conn, 1, cur); err == nil {
			t.Fatal("monotone gate must REFUSE a silently dropped trusted-for statement")
		}
	})
}

// --- coverage-as-data ---------------------------------------------------------

// tcbCov is one native_tcb_coverage row: a threat class's seeded fixtures, the
// control that catches them, and the irreducible-TCB statements it is trusted for.
type tcbCov struct {
	class      string
	fixtures   []string
	caughtBy   string
	trustedFor []string
}

// computeTCBCoverage projects the seeded corpus into one row per threat class.
func computeTCBCoverage() []tcbCov {
	vaultFx := []string{}
	for _, f := range nativetcb.VaultLeakFixtures {
		vaultFx = append(vaultFx, f.Name)
	}
	vaultFx = append(vaultFx, "storage-mask-reveal-shred", "counterfactual-exfil")

	contractFx := []string{}
	for _, f := range nativetcb.ContractViolFixtures {
		contractFx = append(contractFx, f.Name)
	}

	effectFx := []string{}
	for _, f := range nativetcb.EffectFixtures {
		effectFx = append(effectFx, f.Name)
	}
	effectFx = append(effectFx, "unrecorded-external-residue")

	return []tcbCov{
		{
			class:    nativetcb.ClassVaultLeak,
			fixtures: vaultFx,
			caughtBy: "V2 pii-flow over the caller's AST + the six masking leaves; the vault storage layer (ciphertext-only, grant-gated reveal, crypto-shred)",
			trustedFor: []string{
				"capability egress sinks (mail.send/http) hold real authority over a grant-gated REVEALED value — V2 bounds the unmasked-Vault-in path, not post-reveal re-exfiltration",
				"std/crypto AES-256-GCM is trusted to be the vetted AEAD; the per-subject key-token KDF and seal path are trusted; crypto-shred makes the ciphertext undecryptable by construction",
				"RESIDUE_LOG_SINK: std/log.write bears no capability in the D0 roster, so V2's capability-keyed sink set does not yet include it (contradicts ADR-10 §3) — named residue, not silently passed",
			},
		},
		{
			class:    nativetcb.ClassContractViol,
			fixtures: contractFx,
			caughtBy: "the ADR-04 §6 regel-native differential oracle (production machine vs. independent reference reducer)",
			trustedFor: []string{
				"the INTERNAL content of each derivation pass (that the vault route actually routes, that the policy predicate actually scopes) is trusted — V6 checks pass presence, not pass body correctness",
			},
		},
		{
			class:    nativetcb.ClassEffectOrder,
			fixtures: effectFx,
			caughtBy: "the ADR-10 §6 std-conformance gate in performNative (read-declared native recording an effect fails closed) + the ADR-05 §7 step-transaction outbox accounting",
			trustedFor: []string{
				"a native performing real side-effecting I/O WITHOUT RecordEffect is invisible to the effects-trace conformance and the outbox accounting; native bodies are trusted not to perform unrecorded I/O — ADR-10 §2's H_dispatch attestation bounds this to the vetted roster",
			},
		},
	}
}

func writeTCBCoverage(t *testing.T, w *world, ctx context.Context, epoch int, rows []tcbCov) {
	t.Helper()
	for _, r := range rows {
		if _, err := w.conn.Exec(ctx, `
INSERT INTO native_tcb_coverage (epoch, threat_class, fixture_ids, caught_by, trusted_for)
VALUES ($1,$2,$3::text[],$4,$5::text[])
ON CONFLICT (epoch, threat_class) DO UPDATE
  SET fixture_ids=EXCLUDED.fixture_ids, caught_by=EXCLUDED.caught_by, trusted_for=EXCLUDED.trusted_for`,
			epoch, r.class, r.fixtures, r.caughtBy, r.trustedFor); err != nil {
			t.Fatalf("write tcb coverage %s: %v", r.class, err)
		}
	}
}

// assertTCBMonotone is the native-TCB coverage-monotonicity gate (ADR-10 §8): a
// class present in a prior epoch may not vanish; its fixture inventory may not
// shrink; and a trusted-for statement may not silently disappear. A regression
// fails the release.
func assertTCBMonotone(ctx context.Context, q catalog.Querier, epoch int, cur []tcbCov) error {
	byClass := map[string]tcbCov{}
	for _, c := range cur {
		byClass[c.class] = c
	}
	rows, err := q.Query(ctx,
		`SELECT DISTINCT threat_class FROM native_tcb_coverage WHERE epoch < $1`, epoch)
	if err != nil {
		return err
	}
	var priorClasses []string
	for rows.Next() {
		var pc string
		if err := rows.Scan(&pc); err != nil {
			rows.Close()
			return err
		}
		priorClasses = append(priorClasses, pc)
	}
	rows.Close()

	for _, pc := range priorClasses {
		c, ok := byClass[pc]
		if !ok {
			return fmt.Errorf("MONOTONE VIOLATION: threat class %q was covered in a prior epoch but is absent from epoch %d", pc, epoch)
		}
		var pfx, ptf []string
		found, err := q.QueryRow(ctx, `
SELECT fixture_ids, trusted_for FROM native_tcb_coverage
WHERE threat_class=$1 AND epoch < $2 ORDER BY epoch DESC LIMIT 1`,
			[]any{pc, epoch}, &pfx, &ptf)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		for _, f := range pfx {
			if !containsStr(c.fixtures, f) {
				return fmt.Errorf("MONOTONE VIOLATION: threat class %q dropped fixture %q (epoch <%d → %d)", pc, f, epoch, epoch)
			}
		}
		for _, s := range ptf {
			if !containsStr(c.trustedFor, s) {
				return fmt.Errorf("MONOTONE VIOLATION: threat class %q silently dropped a trusted-for statement", pc)
			}
		}
	}
	return nil
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// --- differential-oracle observable projection (harness-owned) ----------------

func oracleCaseByName(name string) (oracle.Case, bool) {
	for _, c := range oracle.Corpus {
		if c.Name == name {
			return c, true
		}
	}
	return oracle.Case{}, false
}

func cekArgs(in []any) []cek.Value {
	out := make([]cek.Value, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case float64:
			out = append(out, cek.NumV(v))
		case string:
			out = append(out, cek.StrV(v))
		case bool:
			out = append(out, cek.BoolV(v))
		default:
			panic("nativetcb: corpus input kind not covered")
		}
	}
	return out
}

func oracleArgs(in []any) []oracle.Value {
	out := make([]oracle.Value, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case float64:
			out = append(out, oracle.Value{Kind: oracle.VNum, Num: v})
		case string:
			out = append(out, oracle.Value{Kind: oracle.VStr, Str: v})
		case bool:
			out = append(out, oracle.Value{Kind: oracle.VBool, Bool: v})
		default:
			panic("nativetcb: corpus input kind not covered")
		}
	}
	return out
}

// cekVerdict / refVerdict project each evaluator's outcome to a comparable string;
// a violation maps to the same "park:contract.<clause>.violated" tag both sides
// use, so an honest run agrees and a contract-violating native diverges.
func cekVerdict(o cek.Outcome) string {
	switch o.Kind {
	case cek.OutDone:
		return "value:" + renderCekVal(o.Value)
	case cek.OutParked:
		if o.Condition != nil {
			return "park:" + o.Condition.Class
		}
		return "park:"
	case cek.OutFaulted:
		return "throw"
	default:
		return "error"
	}
}

func refVerdict(r oracle.Result) string {
	switch r.Kind {
	case "value":
		return "value:" + oracle.Render(r.Value)
	case "violation":
		return "park:contract." + r.Clause + ".violated"
	case "throw":
		return "throw"
	default:
		return "error"
	}
}

func renderCekVal(v cek.Value) string {
	switch v.Tag {
	case cek.TagUndefined:
		return "undefined"
	case cek.TagNull:
		return "null"
	case cek.TagBool:
		if v.N != 0 {
			return "true"
		}
		return "false"
	case cek.TagF64:
		return "num:" + oracle.NumToStr(v.N)
	case cek.TagStr:
		return strconv.Quote(v.S)
	default:
		return "<opaque>"
	}
}
