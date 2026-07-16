package m5eval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// pin.go implements the REVIEW-PRE-E §4 L2 fix: "pass@k floor gameable via retry
// ceiling — PIN k PER EPOCH as rows". k is not an operator dial you can turn up
// after seeing the numbers; it is frozen in an eval_pin row bound to the corpus
// hash. Changing k, or editing the corpus, changes the hash → the harness detects
// a stale/tampered pin and refuses to score against it (a new pin + re-run is
// required). This is the whole point of pinning: the reported pass@k is a claim
// about a specific (k, corpus) pair, provable after the fact.

// AuthoringCorpusHash is the canonical content hash of the authoring corpus: the
// ordered (ID, Module, Entry, Signature, Reference, Inputs) of every task. It
// deliberately EXCLUDES nothing load-bearing — a changed reference or input set
// yields a new hash, so a pin can never silently drift onto a mutated corpus.
func AuthoringCorpusHash() string {
	var b strings.Builder
	for _, t := range AuthoringCorpus {
		fmt.Fprintf(&b, "A|%s|%s|%s|%s|%s|%v\n", t.ID, t.Module, t.Entry, t.Signature, t.Reference, t.Inputs)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "authoring:" + hex.EncodeToString(sum[:])
}

// RestartCorpusHash is the canonical content hash of the restart corpus.
func RestartCorpusHash() string {
	var b strings.Builder
	for _, s := range RestartCorpus {
		u := append([]string(nil), s.Unsafe...)
		sort.Strings(u)
		fmt.Fprintf(&b, "R|%s|%s|%s|%v|%s|%v\n", s.ID, s.Class, s.Message, s.Restarts, s.Correct, u)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "restart:" + hex.EncodeToString(sum[:])
}

// Floors — the ADR-12 §3a / §7 release floors (data, not magic numbers in code).
const (
	FloorPassAt1     = 0.5  // ADR-12 §3a
	FloorPassAtK     = 0.9  // ADR-12 §3a
	FloorRestartAcc  = 0.95 // ADR-12 §7
	FloorAuthoringN  = 50   // ADR-12 §3a task-suite floor (N≥50)
	FloorRestartM    = 30   // ADR-12 §7 scenario-suite floor (M≥30)
	FuelMargin       = 1.5  // ADR-12 §5 margin
	CostFullPipeline = 5.0  // deepest-stage admission-fuel cost (internal/admission stageCost)
)

// PinnedK is the per-epoch retry ceiling for pass@k. Pinned as a row (see
// EnsurePin); this constant is the value written at pin time. Changing it is a
// new pin (the L2 fix: k is frozen with the corpus, not tuned post-hoc).
const PinnedK = 3
