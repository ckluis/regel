package kernel

import (
	"context"
	"strings"
	"testing"

	"regel.dev/regel/internal/admission"
)

// contract_discharge_test.go is the BUILD-C runtime discharge of the V4-derived
// boundary validators (ADR-04 §6 layer b prerequisite; ADR-07 §4 V4 "every call
// site the catalog wires discharges them as boundary validators"): the contract
// clauses of an ADMITTED definition actually RUN when the definition is
// evaluated through the kernel eval path — a pre clause refuses entry, a post
// clause refuses exit, each as a TYPED durable-condition park
// (contract.pre.violated / contract.post.violated), and a pre violation fires
// NO effect.

const shipSrc = `import { pre, post } from "std/contract";
import { send } from "std/mail";
export function ship(qty: number): number {
  pre(qty > 0);
  send("wh@example.com", "pick");
  post(qty < 1000);
  return qty * 2;
}
`

func TestContractBoundaryValidatorsRunAtEval(t *testing.T) {
	ts, pool := testServer(t)
	ctx := context.Background()

	// mail.send must be declared + granted so V1 admits the definition.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','mail.send','','test')`); err != nil {
		pool.Release(conn)
		t.Fatal(err)
	}
	patch := admission.Patch{
		Modules:              []admission.ModuleSrc{{ModuleName: "app/disch", Source: shipSrc}},
		DeclaredCapabilities: map[string][]string{"app/disch/ship": {"mail.send"}},
		TargetScope:          admission.Scope{Kind: 0, ID: ""},
		BaseHashes:           map[string]string{},
	}
	v, err := admission.Admit(ctx, conn, patch,
		admission.Principal{ActorKind: "engineer", ActorID: "dev", Via: "cli"}, admission.BuildImage())
	pool.Release(conn)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit ship: %q %+v", v.Outcome, v.Diagnostics)
	}

	count := func(q string, args ...any) int {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Release(c)
		var n int
		if _, err := c.QueryRow(ctx, q, args, &n); err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		return n
	}

	// 1. Satisfying input: both boundary validators pass; the def evaluates.
	code, body, _ := post(t, ts.URL+"/eval/app/disch/ship", `[3]`)
	if code != 200 || strings.TrimSpace(body) != "6" {
		t.Fatalf("passing eval: code=%d body=%q (want 200 / 6)", code, body)
	}

	// 2. Pre violation: entry refused as a TYPED durable-condition park; the
	//    park is durable (a condition row exists) and NO effect fired.
	outboxBefore := count(`SELECT count(*) FROM outbox`)
	code, body, _ = post(t, ts.URL+"/eval/app/disch/ship", `[-1]`)
	if code != 202 {
		t.Fatalf("pre-violating eval: code=%d body=%q (want 202 park)", code, body)
	}
	if !strings.Contains(body, "contract.pre.violated") {
		t.Fatalf("pre violation not typed: %q", body)
	}
	if n := count(`SELECT count(*) FROM durable_condition WHERE class='contract.pre.violated'`); n != 1 {
		t.Fatalf("pre violation not durable: %d condition rows", n)
	}
	if got := count(`SELECT count(*) FROM outbox`); got != outboxBefore {
		t.Fatalf("a pre violation fired an effect: outbox %d -> %d", outboxBefore, got)
	}

	// 3. Post violation: exit refused likewise, with the post clause subject.
	code, body, _ = post(t, ts.URL+"/eval/app/disch/ship", `[2000]`)
	if code != 202 || !strings.Contains(body, "contract.post.violated") {
		t.Fatalf("post-violating eval: code=%d body=%q (want 202 contract.post.violated)", code, body)
	}
	if n := count(`SELECT count(*) FROM durable_condition WHERE class='contract.post.violated'`); n != 1 {
		t.Fatalf("post violation not durable: %d condition rows", n)
	}
}
