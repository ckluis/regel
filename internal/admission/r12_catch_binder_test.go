package admission

import "testing"

// r12_catch_binder_test.go — STAGE-F R12 (STAGE-E §9 residue): V2's catch-binder
// taint was conservatively imprecise. The old trigger `throwsNonLiteral` tainted
// the catch binder whenever the try block threw ANYTHING that was not a bare
// literal atom (KNum/KStr/KBool/…). A throw of a COMPOSITE built entirely from
// literals — a binary/concat or a template of literals — carries no vault value,
// yet the catch binder was tainted and a subsequent boundary sink over it was
// falsely REJECTED. R12 tightens the trigger to `provablyCleanThrow`: the binder
// stays clean iff every syntactic throw in the try block is provably pii-free
// (built with ZERO variable/call/member references, so it cannot carry a vault
// value).
//
// Every relaxation is GUARDED by an adjacent hostile fixture that still REJECTS:
// the moment a thrown composite references a vault binder, the whole throw is
// unclean, the catch binder is tainted, and the sink over it is refused.
//
// The catch binder is typed `unknown`, so each fixture narrows with the dialect's
// sanctioned `typeof e === "string"` guard (`as` casts are banned) before the
// std/log.write(string) sink — the taint is a binder property, so it survives the
// narrowing.

// --- Relaxation 1: binary-of-literals throw, logged in catch — must ADMIT -------
// `throw "err-" + "42"` is a KBinary over two string literals → provably clean.
// Old throwsNonLiteral tainted `e` (KBinary is not a literal atom) → write(e)
// flagged PII_ESCAPE. Provably clean: the concat references no binder.
func TestR12SafeBinaryThrowCatchAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { write } from "std/log";
export function safeBin(n: number): void {
  try {
    if (n > 0) {
      throw "err-" + "42";
    }
  } catch (e) {
    if (typeof e === "string") {
      write(e);
    }
  }
}
`
	v, err := admit(ctx, w.conn, src, "app/r12/safebin", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("binary-of-literals throw logged in catch was rejected (false positive): %q diags=%+v",
			v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/r12/safebin/safeBin'"); got != 1 {
		t.Fatalf("admitted def pointer missing (%d)", got)
	}
}

// --- Hostile 1 (guards relaxation 1): pii concatenated into the throw — REJECT --
// `throw "err: " + owner` references a Vault binder → unclean → binder tainted →
// write(e) leaks the concatenated ssn string. A real PII escape; stays REJECTED.
func TestR12HostileBinaryThrowCatchRejects(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { write } from "std/log";
import type { Vault } from "std/pii";
export function hostileBin(owner: Vault<string>, n: number): void {
  try {
    if (n > 0) {
      throw "err: " + owner;
    }
  } catch (e) {
    if (typeof e === "string") {
      write(e);
    }
  }
}
`
	v, err := admit(ctx, w.conn, src, "app/r12/hostilebin", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("pii-bearing binary throw admitted (ESCAPE): %q diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" || v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 PII_ESCAPE, got %+v", v.Diagnostics)
	}
}

// --- Relaxation 2 / soundness twin: template throws ----------------------------
// A pii-interpolated template throw — REJECT. The OLD isLiteralNode treated EVERY
// KTemplate as a literal, so a throw of `err ${owner}` produced a CLEAN catch
// binder and write(e) ADMITTED — a latent escape (a template interpolates a vault
// value). provablyCleanThrow requires each interpolated part to be clean, so this
// now REJECTS.
func TestR12HostileTemplateThrowCatchRejects(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { write } from "std/log";
import type { Vault } from "std/pii";
export function hostileTmpl(owner: Vault<string>, n: number): void {
  try {
    if (n > 0) {
      throw ` + "`err ${owner}`" + `;
    }
  } catch (e) {
    if (typeof e === "string") {
      write(e);
    }
  }
}
`
	v, err := admit(ctx, w.conn, src, "app/r12/hostiletmpl", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("pii-interpolated template throw admitted (ESCAPE): %q diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" || v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 PII_ESCAPE, got %+v", v.Diagnostics)
	}
}

// Negative twin of relaxation 2: literal-interpolation template still ADMITS.
// `err ${42}` interpolates a LITERAL — provably clean (no reference), must ADMIT.
// Under the old code this admitted too, but for the WRONG reason (every template
// was blanket-clean); here it admits because the interpolated part is a literal.
func TestR12SafeTemplateThrowCatchAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { write } from "std/log";
export function safeTmpl(n: number): void {
  try {
    if (n > 0) {
      throw ` + "`err ${42}`" + `;
    }
  } catch (e) {
    if (typeof e === "string") {
      write(e);
    }
  }
}
`
	v, err := admit(ctx, w.conn, src, "app/r12/safetmpl", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("literal-interpolation template throw rejected (false positive): %q diags=%+v",
			v.Outcome, v.Diagnostics)
	}
}

// Named residue (fail-closed boundary): a template interpolating a VARIABLE stays
// conservatively tainted even when the variable is a non-pii number. Proving `n`
// clean needs env/scope-resolved taint at the throw site (De Bruijn indices shift
// through nested scopes), which provablyCleanThrow deliberately does NOT do — it
// is a purely syntactic, env-independent predicate. So `err ${n}` keeps the catch
// binder tainted and write(e) is REJECTED. This is sound (never admits pii) and is
// the stated remaining conservatism: R12 relaxes only reference-FREE throws.
func TestR12VarInterpTemplateStaysConservative(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { write } from "std/log";
export function conservTmpl(n: number): void {
  try {
    if (n > 0) {
      throw ` + "`err ${n}`" + `;
    }
  } catch (e) {
    if (typeof e === "string") {
      write(e);
    }
  }
}
`
	v, err := admit(ctx, w.conn, src, "app/r12/consvtmpl", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("var-interpolation template expected conservatively rejected, got %q diags=%+v",
			v.Outcome, v.Diagnostics)
	}
}
