# regel

**The code is rows. The rows are TypeScript.**

regel is a governed *code-as-rows* substrate: one Go kernel and one PostgreSQL
database, where an application is written in a closed-world, strict dialect of
TypeScript, admitted into the catalog by transaction, and stored as
content-addressed rows. Code pauses as durable continuations and resumes on any
kernel. **Deploying is a commit; rolling back is an `as-of` `WHERE` clause.**

> **Status — v1.1, gate-green pilot.** Stages A–F are complete: the admission
> pipeline, the continuation machine, the six in-transaction verifiers, the
> `std/` world as genesis rows, the reactive UI, and a proof CRM built entirely
> as admitted rows all pass their gates against real PostgreSQL. It is an
> honest pilot on one node at small scale — **not yet** the any-scale,
> lives-forever substrate the vision page imagines. A five-lens review council
> stress-tested the claims; where the evidence is simulated rather than
> elapsed, [`spec/FINAL.md`](spec/FINAL.md) and
> [`spec/gates/STAGE-F.md`](spec/gates/STAGE-F.md) §6a say so plainly.

The vision and the honest accounting of what got built live in
[`index.html`](index.html) (also served as the project's preview page).

## The idea

- **Admission, not deployment.** Code enters through one gate — a single
  `SERIALIZABLE` transaction that runs the verifiers, type-checks in-process,
  and writes the definition, its DDL, and the name pointer together, or not at
  all. Engineers, tenants, and AI agents all walk through the same door.
- **Identity is content, not text.** A definition's identity is a SHA-256 over
  its normalized AST (De Bruijn indices, types and contracts in the hash;
  comments and local names out). Two spellings of the same program are the same
  row. The git repo is a deterministic fold over the admission ledger —
  byte-identical SHAs are the release gate.
- **Pause is data.** An owned, defunctionalized CEK interpreter runs the
  dialect; `await` and fuel exhaustion park the machine as a durable condition
  row (a versioned binary continuation frame). A workflow killed mid-step
  resumes on a different kernel to the identical result, effects exactly-once.
- **The verifier suite is the security boundary** — not the type system. Six
  verifiers (capability, PII-flow, catalog-parity, contracts, capture,
  derivation-parity) run in the admission transaction against a hostile fixture
  corpus and dual mutation testing.
- **The world is rows too.** `std/` ships as genesis rows; the UI is a reactive
  fold over the catalog (server-rendered templates, binary SSE deltas); agents
  are ordinary capability principals over an MCP plane.

See [`spec/architecture/`](spec/architecture/) for the 13 ADRs that define all
of this, and [`docs/claim-evidence.md`](docs/claim-evidence.md) for the 31
concept claims mapped to a test, a demo, or a named residue.

## Repository layout

| Path | What |
|---|---|
| `cmd/regel/` | the single binary (CLI + `serve`) |
| `internal/` | kernel: `cek/` interpreter, `cfr/` continuation frames, `admission/` verifiers, `catalog/`, `pgwire/` (owned Postgres wire client), `mcp/`, `gitproj/`, `kernel/`, `ui/` |
| `crm/` | the proof CRM — an app authored entirely as admitted rows |
| `std/` definitions, `gate/` | the standard library and the native-TCB / eval gates |
| `scripts/` | runnable demos and the five end-to-end scenarios |
| `spec/` | ADRs, gate reports, the orchestration ledger, `FINAL.md` |
| `third_party/typescript-go/` | vendored TypeScript-Go (Apache-2.0), the trusted type-checker |

## Quickstart

**Prerequisites:** Go 1.26+ and a local PostgreSQL 16.x.

The kernel finds Postgres through the `REGEL_PG_DSN` environment variable:

```sh
export REGEL_PG_DSN="postgres://<user>@localhost:5432/regel"
```

Run the test suite (it exercises a real database; serialize packages to avoid
cross-package scratch-DB contention):

```sh
go test -p 1 ./...
```

Walk the Stage-A skeleton end to end — admit, evaluate, roll back, park and
restart on fuel — against a fresh, disposable `regel_demo` database:

```sh
./scripts/demo-admit-rollback.sh
```

Other entry points worth reading: `scripts/demo-kill9-resume.sh` (a real
`kill -9` mid-workflow, resumed exactly-once), `scripts/scenario-b-agent-patch.sh`
(an agent authoring a patch over MCP), and `scripts/scenario-d-asof-rollback.sh`
(rollback observed through the UI). The `regel` binary itself exposes
`migrate-db`, `genesis`, `serve`, `admit`, `eval`, `grant`, `approve`, `mcp`,
`project`, `shred`, and more.

## What's proven, and what v2 holds

The build discipline is *red-path-first*: a capability is only "done" once the
test that fails **without** it has been witnessed failing. Every claim traces to
a captured artifact under [`evidence-f/`](evidence-f/) or a gate report under
[`spec/gates/`](spec/gates/).

What is genuinely proven: exactly-once across a real cross-process crash,
byte-identical git projection and genesis, the six verifiers against a hostile
corpus, and a CRM with zero business logic in Go. What is **provisioned but not
yet exercised** — and named as such — includes cross-version continuation
resume, single-node scale limits, and an agent-authoring corpus beyond toy
functions. The full v2 backlog, including the review council's findings, is
[`spec/gates/STAGE-F.md`](spec/gates/STAGE-F.md) §6/§6a.

## License

MIT — see [`LICENSE`](LICENSE). regel's own source is original work. It vendors
TypeScript-Go under Apache-2.0 (© Microsoft) and depends on permissive
BSD/MIT-licensed Go modules; the full accounting is in
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md). "TypeScript" is a trademark
of Microsoft Corporation; regel implements a dialect and is not affiliated with
or endorsed by Microsoft.
