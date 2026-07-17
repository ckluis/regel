package admission

import (
	"fmt"
	"testing"
)

// c4_controlflow_test.go — STAGE-E C4 (STAGE-C §10.4 residue): V2 (pii-flow) and
// V5 (capture) dataflow must cover the FULL statement grammar the dialect admits.
// Before the fix the walkers handled only KVarDecl/KReturn/KExprStmt/KIf/KBlock —
// a statement inside for / for-of / while / do-while / switch / try was NEVER
// WALKED, so a pii escape or an unserializable capture smuggled through any of
// those constructs was ADMITTED blind (RED captured on each fixture below by
// running it against the pre-fix walker).

// --- V2: pii escapes through unwalked constructs -------------------------------

func c4V2Fixtures() []struct{ name, module, src string } {
	return []struct{ name, module, src string }{
		{"while-body return escape", "app/c4/w", `import type { Vault } from "std/pii";
export function leakWhile(owner: Vault<string>, n: number): string {
  while (n > 0) {
    return owner;
  }
  return "ok";
}
`},
		{"for-body return escape", "app/c4/f", `import type { Vault } from "std/pii";
export function leakFor(owner: Vault<string>, n: number): string {
  for (let i = 0; i < n; i = i + 1) {
    return owner;
  }
  return "ok";
}
`},
		{"switch-clause return escape", "app/c4/s", `import type { Vault } from "std/pii";
export function leakSwitch(owner: Vault<string>, k: number): string {
  switch (k) {
    case 1:
      return owner;
  }
  return "ok";
}
`},
		{"try-block return escape", "app/c4/t", `import type { Vault } from "std/pii";
export function leakTry(owner: Vault<string>, n: number): string {
  try {
    if (n > 0) {
      return owner;
    }
  } catch {
  }
  return "ok";
}
`},
		{"for-of element escape", "app/c4/fo", `import type { Vault } from "std/pii";
export function leakForOf(owner: Vault<string>): string {
  for (const x of [owner]) {
    return x;
  }
  return "ok";
}
`},
		{"do-while body escape", "app/c4/dw", `import type { Vault } from "std/pii";
export function leakDoWhile(owner: Vault<string>, n: number): string {
  do {
    if (n > 0) {
      return owner;
    }
  } while (n > 0);
  return "ok";
}
`},
	}
}

func TestC4V2FullControlFlowEscapesReject(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	for _, fx := range c4V2Fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			v, err := admit(ctx, w.conn, fx.src, fx.module, engineer("dev"), nil)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if v.Outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want rejected (PII_ESCAPE through %s); diags=%+v",
					v.Outcome, fx.name, v.Diagnostics)
			}
			if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" {
				t.Fatalf("want PII_ESCAPE, got %+v", v.Diagnostics)
			}
			if v.Diagnostics[0].StageOrVerifier != "V2" {
				t.Fatalf("want V2, got %q", v.Diagnostics[0].StageOrVerifier)
			}
		})
	}
}

// The NEGATIVE twins: the same constructs carrying only non-pii values must still
// admit — coverage must not become a blanket ban (no false positives).
func TestC4V2FullControlFlowCleanAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import type { Vault } from "std/pii";
export function cleanFlow(owner: Vault<string>, n: number): string {
  let acc = "";
  while (n > 3) {
    return "big";
  }
  for (let i = 0; i < n; i = i + 1) {
    acc = acc + "x";
  }
  for (const s of ["a", "b"]) {
    acc = acc + s;
  }
  switch (n) {
    case 1:
      return "one";
  }
  try {
    acc = acc + "!";
  } catch {
  }
  return acc;
}
`
	v, err := admit(ctx, w.conn, src, "app/c4/neg", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("clean control flow rejected (false positive): %q diags=%+v", v.Outcome, v.Diagnostics)
	}
}

// --- V5: unserializable captures through unwalked constructs -------------------

func c4V5Fixtures() []struct{ name, module, src string } {
	return []struct{ name, module, src string }{
		{"await inside while, conn used after", "app/c4v5/w", `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(n: number): Promise<Conn> {
  const c: Conn = connect();
  while (n > 0) {
    await sleep(1);
    n = n - 1;
  }
  return c;
}
`},
		{"await inside for, conn used after", "app/c4v5/f", `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(n: number): Promise<Conn> {
  const c: Conn = connect();
  for (let i = 0; i < n; i = i + 1) {
    await sleep(1);
  }
  return c;
}
`},
		{"loop-carried use before textual await", "app/c4v5/lc", `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(n: number): Promise<number> {
  const c: Conn = connect();
  let total = 0;
  while (n > 0) {
    total = total + probe(c);
    await sleep(1);
    n = n - 1;
  }
  return total;
}
export function probe(c: Conn): number {
  return 1;
}
`},
		{"await inside try, conn used after", "app/c4v5/t", `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(n: number): Promise<Conn> {
  const c: Conn = connect();
  try {
    await sleep(1);
  } catch {
  }
  return c;
}
`},
	}
}

func TestC4V5FullControlFlowCapturesReject(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	for i, fx := range c4V5Fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			v, err := admit(ctx, w.conn, fx.src, fx.module, engineer("dev"),
				workflowTier(fmt.Sprintf("%s/wf", fx.module)))
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if v.Outcome != OutcomeRejected {
				t.Fatalf("fixture %d %s: outcome = %q, want rejected (CAPTURE_UNSERIALIZABLE); diags=%+v",
					i, fx.name, v.Outcome, v.Diagnostics)
			}
			found := false
			for _, d := range v.Diagnostics {
				if d.Code == "CAPTURE_UNSERIALIZABLE" && d.StageOrVerifier == "V5" {
					found = true
				}
			}
			if !found {
				t.Fatalf("want a V5 CAPTURE_UNSERIALIZABLE diagnostic, got %+v", v.Diagnostics)
			}
		})
	}
}

// Negative twin: awaits inside loops with only serializable state admit; a conn
// scoped entirely BEFORE the loop's await span (reacquired per iteration, used
// before its iteration's await… is still live at that await's capture per the
// straight-line rule, so the clean twin simply keeps conns out of loop bodies).
func TestC4V5FullControlFlowCleanAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { sleep } from "std/wf";
export async function wf(n: number): Promise<number> {
  let total = 0;
  while (n > 0) {
    await sleep(1);
    total = total + 1;
    n = n - 1;
  }
  for (let i = 0; i < 3; i = i + 1) {
    await sleep(1);
    total = total + i;
  }
  return total;
}
`
	v, err := admit(ctx, w.conn, src, "app/c4v5/neg", engineer("dev"), workflowTier("app/c4v5/neg/wf"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("clean loop workflow rejected (false positive): %q diags=%+v", v.Outcome, v.Diagnostics)
	}
}
