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
	Kind int `json:"kind"`
	Name string `json:"name"`
	CFR  int `json:"cfr"`
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
	var firstBlob string
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".cfr") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(goldenDir, ent.Name()))
		if err := os.WriteFile(filepath.Join(tmp, ent.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if firstBlob == "" {
			firstBlob = ent.Name()
		}
	}
	if firstBlob == "" {
		t.Fatal("no blobs copied")
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
	// Clear stale blobs.
	old, _ := filepath.Glob(filepath.Join(goldenDir, "*.cfr"))
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
