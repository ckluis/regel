# Third-Party Notices

regel is licensed under the MIT License (see `LICENSE`). It redistributes and
depends on third-party software listed below. All components are under
permissive licenses compatible with distributing this project under MIT; the
Apache-2.0 and MIT sub-components below retain their own licenses within their
subtrees, and their notices are reproduced/retained as required.

No component is under a copyleft license (no GPL / LGPL / AGPL / MPL / CDDL /
EPL / SSPL). The audit that established this is summarized at the end.

## Redistributed in this repository (vendored source)

### github.com/microsoft/typescript-go  — `third_party/typescript-go/`
- **License:** Apache License 2.0
- **Copyright:** © Microsoft Corporation
- The full license text is retained at `third_party/typescript-go/LICENSE`, and
  the attribution notice at `third_party/typescript-go/NOTICE.txt` is retained
  and propagated as required by Apache-2.0 §4(d).
- Pinned upstream revision: `v0.0.0-20260709225601-168e7015edf9`
  (go.mod `replace` → `./third_party/typescript-go`).
- Contains an MIT-licensed sub-component (a filesystem watcher) at
  `third_party/typescript-go/internal/fswatch/`, © Microsoft Corporation and
  © 2017-present Devon Govett; its license is retained at
  `third_party/typescript-go/internal/fswatch/LICENSE`.

## Build-time Go module dependencies (not vendored in this repository)

Pulled by the Go toolchain at build time; their license texts should be
included when distributing compiled **binaries**.

| Module | License |
|---|---|
| github.com/microsoft/typescript-go | Apache-2.0 (vendored — see above) |
| github.com/go-json-experiment/json | BSD-3-Clause |
| github.com/klauspost/cpuid/v2 | MIT |
| github.com/zeebo/xxh3 | BSD-2-Clause |
| golang.org/x/sync | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| golang.org/x/text | BSD-3-Clause |

(`github.com/zeebo/assert`, a CC0 / public-domain test-only transitive
dependency of `zeebo/xxh3`, is not linked into shipped binaries.)

## Runtime dependency (not distributed)

- **PostgreSQL 16.x** — connected to over the wire protocol by regel's own
  client; not bundled or redistributed by this project. PostgreSQL is under the
  permissive PostgreSQL License.

## Trademark note

"TypeScript" is a trademark of Microsoft Corporation. regel implements a
closed-world, strict *dialect* of TypeScript and references the name
nominatively; this project is not affiliated with, sponsored by, or endorsed by
Microsoft, and the Apache-2.0 license of typescript-go grants no trademark
rights (Apache-2.0 §6).

## Audit summary (2026-07-18)

- regel's own source (`internal/`, `cmd/`, `gate/`, `crm/`, `spec/`, `scripts/`,
  `std/` definitions) carries no foreign copyright headers and copies no
  third-party code — it is original work, freely MIT-licensable.
- The only third-party code redistributed in this repo is the vendored
  Apache-2.0 typescript-go (with its MIT fswatch sub-component). Both are
  permissive; the project as a whole distributes under MIT while those subtrees
  retain their own licenses.
- All build-time Go dependencies are permissive (BSD/MIT/CC0); none copyleft.
- Open follow-ups (hygiene, not blockers): (1) verify the vendored
  typescript-go tree is byte-for-byte the pinned upstream revision, or mark any
  changed files per Apache-2.0 §4(b); (2) generate a full SBOM before a tagged
  public release.
