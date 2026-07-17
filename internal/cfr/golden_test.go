package cfr

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"regel.dev/regel/internal/cek"
)

// regenFlag gates the golden-corpus generator so a normal `go test` never
// rewrites committed testdata. Set via -regen or REGEL_REGEN_GOLDEN=1. Flags are
// parsed by testing.Main before any test runs, so it is read inside the test.
var regenFlag = flag.Bool("regen", false, "regenerate the golden-continuation corpus + manifest")

func regenRequested() bool {
	return os.Getenv("REGEL_REGEN_GOLDEN") == "1" || (regenFlag != nil && *regenFlag)
}

// golden_test.go — the ADR-05/ADR-08 GOLDEN-CONTINUATION CORPUS (residue B2): a
// committed corpus of encoded CFR blobs covering every K frame kind at every CFR
// version in production, with a DECODE-COVERAGE MONOTONE FLOOR. Every golden blob
// must decode in the current binary, and the set of (frame kind, cfr version)
// pairs it covers must NEVER shrink below the committed manifest — a dropped or
// corrupted blob, or a decoder that stops accepting a historical frame kind,
// turns the floor red before that binary ships (O2/O3, ADR-08 §4).
//
// Regenerate deliberately (a new frame kind or CFR version is an epoch change):
//
//go:generate go test -run TestGenerateGoldenCorpus -regen ./...
//
// or: REGEL_REGEN_GOLDEN=1 go test -run TestGenerateGoldenCorpus ./internal/cfr/

const goldenDir = "testdata/golden"
const manifestFile = "testdata/golden/coverage.json"

// coveragePair is one committed (frame kind, cfr version) obligation.
type coveragePair struct {
	Kind int    `json:"kind"`
	Name string `json:"name"`
	CFR  int    `json:"cfr"`
}

func (p coveragePair) key() [2]int { return [2]int{p.Kind, p.CFR} }

// frameKindName is the stable, drift-proof identifier for a frame kind (its
// ordinal, which is append-only and CFR-versioned — cek/frame.go).
func frameKindName(k int) string { return fmt.Sprintf("fr%02d", k) }

// buildGoldenState synthesizes a minimal-but-real machine State whose K stack
// holds exactly one frame of the given kind, carrying a value and a one-slot env
// so the blob exercises the value + env + frame decoders together. Node is left
// nil — Decode re-derives it lazily against the catalog at resume (decode.go), so
// a golden blob round-trips standalone.
func buildGoldenState(kind int) *cek.State {
	env := cek.NewEnv(nil, []cek.Value{cek.StrV("golden")})
	return &cek.State{
		Val: cek.NumV(1),
		Env: env,
		Kont: []*cek.Frame{{
			Kind: cek.FrameKind(kind),
			Env:  env,
			Vals: []cek.Value{cek.NumV(float64(kind)), cek.BoolV(true)},
		}},
	}
}

// productionFrameKinds enumerates every valid frame kind in this binary.
func productionFrameKinds() []int {
	var out []int
	for k := 0; cek.FrameKindValid(cek.FrameKind(k)); k++ {
		out = append(out, k)
	}
	return out
}

// checkGoldenCorpus decodes every *.cfr blob in dir and returns the set of
// (frame kind, cfr version) pairs they cover. A blob that fails to decode is an
// error (the corpus is broken). Used by both the floor test and the red-path.
func checkGoldenCorpus(dir string) (map[[2]int]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	covered := map[[2]int]bool{}
	blobs := 0
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".cfr") {
			continue
		}
		blobs++
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("golden blob %s is empty", ent.Name())
		}
		cfrVer := int(data[0])
		st, derr := Decode(data)
		if derr != nil {
			return nil, fmt.Errorf("golden blob %s no longer decodes: %w", ent.Name(), derr)
		}
		for _, f := range st.Kont {
			covered[[2]int{int(f.Kind), cfrVer}] = true
		}
	}
	if blobs == 0 {
		return nil, fmt.Errorf("no golden blobs in %s", dir)
	}
	return covered, nil
}

func loadManifest(t *testing.T) []coveragePair {
	t.Helper()
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatalf("read coverage manifest (regenerate with -regen): %v", err)
	}
	var m []coveragePair
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("coverage manifest is empty")
	}
	return m
}

// TestGoldenContinuationCorpusMonotoneFloor is residue B2: every committed
// (frame kind, cfr version) pair must still decode in this binary. A pair the
// manifest lists but no blob covers = coverage SHRANK = release blocker.
func TestGoldenContinuationCorpusMonotoneFloor(t *testing.T) {
	manifest := loadManifest(t)
	covered, err := checkGoldenCorpus(goldenDir)
	if err != nil {
		t.Fatalf("golden corpus broken: %v", err)
	}
	for _, p := range manifest {
		if !covered[p.key()] {
			t.Fatalf("DECODE-COVERAGE SHRANK: committed pair %s@cfr%d no longer decodes/covered — "+
				"a frame kind was dropped or a golden blob went missing (O2/O3 blocker)", p.Name, p.CFR)
		}
	}
	// The current binary must cover at least the manifest (append-only: a NEW frame
	// kind is allowed above the floor; a removed one below it fails above).
	prod := productionFrameKinds()
	if len(prod) < len(manifest) {
		t.Fatalf("binary has %d frame kinds but the committed floor lists %d — a kind was REMOVED (not append-only)",
			len(prod), len(manifest))
	}
	t.Logf("GOLDEN FLOOR: %d committed (frame-kind,cfr) pairs all decode; binary has %d frame kinds (>= floor)",
		len(manifest), len(prod))
}

// TestGoldenCorpusRedPathCorruption proves the floor can FAIL: a corrupted blob
// stops decoding (checker errors), and a REMOVED blob leaves its manifest pair
// uncovered (floor red). Runs against a temp copy so the committed corpus is
// untouched.
func TestGoldenCorpusRedPathCorruption(t *testing.T) {
	manifest := loadManifest(t)
	tmp := t.TempDir()
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	// Target a SYNTHETIC blob whose single frame kind is covered by no other blob,
	// so removing it genuinely drops a manifest pair. Real-shape blobs (R11) overlap
	// low frame kinds (0,1,3), so the highest-numbered synthetic k-blob is the safe
	// uniquely-covered target.
	var firstBlob string
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".cfr") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(goldenDir, ent.Name()))
		if err := os.WriteFile(filepath.Join(tmp, ent.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(ent.Name(), "k") { // synthetic k00..k29, sorted ascending
			firstBlob = ent.Name() // ends on the highest kind (uniquely covered)
		}
	}
	if firstBlob == "" {
		t.Fatal("no synthetic blob copied")
	}

	// (1) CORRUPT one blob → decode fails → checker errors.
	if err := os.WriteFile(filepath.Join(tmp, firstBlob), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkGoldenCorpus(tmp); err == nil {
		t.Fatal("checker accepted a corrupted golden blob — the floor cannot fail")
	}

	// (2) REMOVE the corrupted blob → its manifest pair is now uncovered → floor red.
	if err := os.Remove(filepath.Join(tmp, firstBlob)); err != nil {
		t.Fatal(err)
	}
	covered, err := checkGoldenCorpus(tmp)
	if err != nil {
		t.Fatalf("remaining corpus should still decode: %v", err)
	}
	var uncovered bool
	for _, p := range manifest {
		if !covered[p.key()] {
			uncovered = true
		}
	}
	if !uncovered {
		t.Fatal("removing a blob left every manifest pair covered — a dropped pair is invisible")
	}
}

// --- R11: real-shape golden coverage + monotone-floor RATCHET --------------------
//
// The 30 synthetic blobs above each carry a single hand-built frame. The R9
// migrate-in-drill (internal/kernel/r9_migrate_std_pair_test.go) parks REAL
// workflows across the new-std-pair epoch boundary, and their captured CFR frames
// are committed here as real_*.cfr — multi-frame continuation SHAPES the corpus
// lacked. real_coverage.json lists them as NAMED floor obligations, so the monotone
// coverage floor RATCHETS above the Stage-E floor: a real blob that goes missing,
// stops decoding, or covers fewer frame kinds than committed turns the floor red.

// goldenSyntheticFloor is the Stage-E committed floor: 30 synthetic single-frame
// blobs. The R11 real-shape entries ratchet the total floor strictly above it.
const goldenSyntheticFloor = 30

const realManifestFile = "testdata/golden/real_coverage.json"

// realShape is one committed real-continuation coverage obligation.
type realShape struct {
	Name  string `json:"name"`
	File  string `json:"file"`
	CFR   int    `json:"cfr"`
	Kinds []int  `json:"kinds"`
}

func loadRealCoverage(t *testing.T) []realShape {
	t.Helper()
	data, err := os.ReadFile(realManifestFile)
	if err != nil {
		t.Fatalf("read real coverage manifest (capture with REGEL_CAPTURE_R11=1): %v", err)
	}
	var m []realShape
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse real manifest: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("real coverage manifest is empty")
	}
	return m
}

// checkRealShapes verifies every committed real-shape obligation against dir: the
// named blob must exist, decode, and still cover every frame kind the manifest
// records for it. A missing/corrupt/shrunk blob returns an error — the floor red.
func checkRealShapes(dir string, shapes []realShape) error {
	for _, s := range shapes {
		data, err := os.ReadFile(filepath.Join(dir, s.File))
		if err != nil {
			return fmt.Errorf("real-shape blob %s missing: %w", s.File, err)
		}
		if len(data) == 0 {
			return fmt.Errorf("real-shape blob %s is empty", s.File)
		}
		st, derr := Decode(data)
		if derr != nil {
			return fmt.Errorf("real-shape blob %s no longer decodes: %w", s.File, derr)
		}
		covered := map[int]bool{}
		for _, f := range st.Kont {
			covered[int(f.Kind)] = true
		}
		for _, k := range s.Kinds {
			if !covered[k] {
				return fmt.Errorf("real-shape blob %s coverage SHRANK: committed frame kind %d no longer present", s.File, k)
			}
		}
	}
	return nil
}

// TestGoldenCorpusRealShapeFloorRatchets is R11: the real-shape obligations all
// hold, and the total floor is strictly above the Stage-E synthetic floor.
func TestGoldenCorpusRealShapeFloorRatchets(t *testing.T) {
	synth := loadManifest(t)
	real := loadRealCoverage(t)
	if err := checkRealShapes(goldenDir, real); err != nil {
		t.Fatalf("real-shape floor red: %v", err)
	}
	floor := len(synth) + len(real)
	if len(synth) != goldenSyntheticFloor {
		t.Fatalf("synthetic floor moved: %d (expected %d) — regenerated corpus?", len(synth), goldenSyntheticFloor)
	}
	if floor <= goldenSyntheticFloor {
		t.Fatalf("floor did NOT ratchet: total %d <= synthetic %d", floor, goldenSyntheticFloor)
	}
	if len(real) < 3 {
		t.Fatalf("expected >= 3 real-shape blobs from the R9 drill, got %d", len(real))
	}
	t.Logf("GOLDEN FLOOR RATCHET (R11): %d -> %d (%d real-continuation shapes added by the R9 migrate drill)",
		goldenSyntheticFloor, floor, len(real))
}

// TestGoldenCorpusRealShapeRedPath proves the ratcheted floor can FAIL: a real
// blob removed below the floor leaves its named obligation uncovered (checker
// errors), and a corrupted real blob stops decoding. Runs against a temp copy.
func TestGoldenCorpusRealShapeRedPath(t *testing.T) {
	real := loadRealCoverage(t)
	tmp := t.TempDir()
	for _, s := range real {
		data, err := os.ReadFile(filepath.Join(goldenDir, s.File))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, s.File), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Baseline: the copied real corpus is green.
	if err := checkRealShapes(tmp, real); err != nil {
		t.Fatalf("copied real corpus should be green: %v", err)
	}
	// (1) REMOVE one real blob → its obligation is uncovered → floor red.
	if err := os.Remove(filepath.Join(tmp, real[0].File)); err != nil {
		t.Fatal(err)
	}
	if err := checkRealShapes(tmp, real); err == nil {
		t.Fatalf("removing real blob %s left the floor green — a regression below the new floor is invisible", real[0].File)
	}
	// (2) CORRUPT another real blob → decode fails → floor red.
	if len(real) > 1 {
		if err := os.WriteFile(filepath.Join(tmp, real[1].File), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkRealShapes(tmp, real); err == nil {
			t.Fatal("corrupting a real blob left the floor green")
		}
	}
}

// TestGenerateGoldenCorpus (re)writes the corpus + manifest. It is a NO-OP unless
// -regen or REGEL_REGEN_GOLDEN=1 is set, so a normal test run never mutates the
// committed testdata. Regenerating is deliberate: a new frame kind or CFR version
// is an epoch change, reviewed like any other.
func TestGenerateGoldenCorpus(t *testing.T) {
	if !regenRequested() {
		t.Skip("set -regen or REGEL_REGEN_GOLDEN=1 to regenerate the golden corpus")
	}
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Clear stale SYNTHETIC blobs only — real-shape blobs (R11, real_*.cfr) are
	// captured from the R9 drill, not regenerated here, and must survive a regen.
	old, _ := filepath.Glob(filepath.Join(goldenDir, "k*.cfr"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	var manifest []coveragePair
	for _, k := range productionFrameKinds() {
		st := buildGoldenState(k)
		blob, err := Encode(st)
		if err != nil {
			t.Fatalf("encode frame kind %d: %v", k, err)
		}
		if _, derr := Decode(blob); derr != nil {
			t.Fatalf("generated blob for kind %d does not round-trip: %v", k, derr)
		}
		name := fmt.Sprintf("k%02d_v%d.cfr", k, FormatVersion)
		if err := os.WriteFile(filepath.Join(goldenDir, name), blob, 0o644); err != nil {
			t.Fatal(err)
		}
		manifest = append(manifest, coveragePair{Kind: k, Name: frameKindName(k), CFR: FormatVersion})
	}
	sort.Slice(manifest, func(i, j int) bool {
		if manifest[i].CFR != manifest[j].CFR {
			return manifest[i].CFR < manifest[j].CFR
		}
		return manifest[i].Kind < manifest[j].Kind
	})
	out, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestFile, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("regenerated golden corpus: %d blobs + manifest at %s", len(manifest), goldenDir)
}
