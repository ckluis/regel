# M5 real-LLM eval capture (ADR-12 §3a/§5/§7) — epoch 1

Captured from real `claude -p` runs driving the real MCP agent plane. Every
number below is computed from persisted m5_eval_result rows. LLM calls (≥): 6;
wall: 17s.

## §3a authoring pass@k  — OPEN (partial — LLM died mid-run; harness left resumable)
- corpus run: N=2 of 15 (full corpus), k=1 (pinned)
- pass@1 = 1.000  (floor ≥ 0.50)
- pass@k = 1.000  (floor ≥ 0.90)
- iterations-to-green P95 = 1.0
- ADR §3a task-suite floor is N≥50; this run is a real sample of the pinned
  corpus (residue: scale-to-N≥50 is operator-schedulable — harness runs at any N).

## §7 restart-decision accuracy  — OPEN (partial — LLM died mid-run; harness left resumable)
- corpus run: M=0 of 31, floor M≥30
- accuracy = 0.000  (floor ≥ 0.95)

## §5 eval-derived fuel capacity
- ADR formula: ceil(P95_iter=1.0 × cost_full_pipeline=5 × margin=1.5) = 8 (the floor)
- provisioned capacity (written to admission_capacity, never throttling): 15
- ADR formula covers a P95-honest passing attempt's fuel: false — formula under-covered (P95_iter is low: each iteration incurs a dry-run AND a commit full-pipeline charge); provisioned capacity was ADJUSTED UP to cover, recorded as a §5 admission

## The mechanized flip (§7)
- restart gate green: false
- agent condition.restart authority enabled (via the real MCP door): false
- consistent: true — agent condition.restart refused RESTART_DISABLED

Flip decision: HELD (restart gate partial/open — agent authority stays DISABLED)
