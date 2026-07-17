# M5 real-LLM eval capture (ADR-12 §3a/§5/§7) — epoch 1

Captured from real `claude -p` runs driving the real MCP agent plane. Every
number below is computed from persisted m5_eval_result rows. LLM calls this
invocation (≥): 0; wall: 0s — a resumed/recompute pass spends few or none; the
cumulative per-attempt record is transcript.jsonl.

## §3a authoring pass@k  — GREEN
- corpus run: N=52 of 52 (full corpus), k=3 (pinned)
- pass@1 = 1.000  (floor ≥ 0.50)
- pass@k = 1.000  (floor ≥ 0.90)
- iterations-to-green P95 = 1.0
- ADR §3a task-suite floor N≥50: MET (N=52 completed — the STAGE-E R5 suite-size residue is discharged)

## §7 restart-decision accuracy  — GREEN
- corpus run: M=31 of 31, floor M≥30
- accuracy = 0.968  (floor ≥ 0.95)

## §5 eval-derived fuel capacity
- ADR formula (BUILD-E R6, +1 commit landing term): ceil((P95_iter=1.0 + 1) × cost_full_pipeline=5 × margin=1.5) = 15 (the floor)
- provisioned capacity (written to admission_capacity, never throttling): 15
- ADR formula covers a P95-honest passing attempt's fuel: true

## The mechanized flip (§7)
- restart gate green: true
- agent condition.restart authority enabled (via the real MCP door): true
- consistent: true — agent condition.restart accepted (status=refused, code=INTERNAL)

Flip decision: FLIPPED — agent-facing condition.restart ENABLED (gate read green)
