package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/rast"
)

// The Stage-A micro-std genesis image (STAGE-A-PLAN pin #3, ADR-10 §1-§2).
//
// One source, four artifacts (ADR-10 §1): the roster below is compiled at
// process start by the real ADR-02 printer/encoder into (i) the genesis row
// image (canonical AST bytes + hashes), (ii) the std-manifest root, (iii) the
// ADR-07 L0 type surface (per-module .ts stubs), and (iv) the hash-keyed native
// dispatch table. Everything is deterministic, so two fresh databases booted on
// the same binary produce byte-identical rows (the §2 reproducibility kill-test).
//
// STAGE-A RESIDUE: each NativeBody carries a placeholder `unknown` signature type
// node — the faithful signatures live in the separately-authored L0 stub surface
// (moduleStub below). A future epoch attaches real signature type nodes and
// re-derives the L0 surface from them (the ADR-10 §1 "one source" ideal).

// Entry is one computed std definition in the genesis image.
type Entry struct {
	CatalogName   string // e.g. "std/mail/send"
	Module        string // e.g. "std/mail"
	Export        string // e.g. "send"
	DefKind       rast.DefKind
	CatalogKind   string // definition.kind column value
	Body          *rast.Node
	Hash          string
	CanonicalText string
	Intrinsic     string      // "std/mail.send"
	Native        cek.NativeFn // nil for type-only entries
	Capability    string      // "" when the binding bears no capability
}

// Image is the whole compiled genesis image.
type Image struct {
	Entries          []*Entry
	ByHash           map[string]*Entry
	CapabilityByHash map[string]string // capability-bearing std hashes → capability
	ModuleStubs      map[string]string // "/std/mail.ts" → L0 stub text
	ManifestRoot     string            // SHA-256 over sorted (name,hash)
	Attestation      string            // H_dispatch (ADR-10 §2)
	Epoch            int
}

var (
	imageOnce sync.Once
	imageInst *Image
)

// BuildImage compiles (once) and returns the deterministic genesis image.
func BuildImage() *Image {
	imageOnce.Do(func() { imageInst = buildImage() })
	return imageInst
}

// Registry builds a fresh native dispatch registry (hash → Go function) from the
// image — the kernel populates the interpreter with this at genesis/boot.
func (im *Image) Registry() *cek.Registry {
	reg := cek.NewRegistry()
	for _, e := range im.Entries {
		if e.Native != nil {
			reg.Register(e.Hash, e.Native)
		}
	}
	return reg
}

// nativeStub is the Stage-A native for std bindings not otherwise exercised
// (taak.all/race/signal/sleep): the dispatch bijection requires a registered
// implementation for every NativeBody hash. It records no effect and returns
// undefined. STAGE-A RESIDUE: real taak join/timer semantics are Stage B.
func nativeStub(_ *cek.Host, _ []cek.Value) (cek.Value, *cek.Condition) {
	return cek.UndefV(), nil
}

// roster is the fixed Stage-A micro-std vocabulary.
type rosterEntry struct {
	module     string
	export     string
	defKind    rast.DefKind
	catKind    string
	native     cek.NativeFn
	capability string
}

func buildImage() *Image {
	roster := []rosterEntry{
		// std/iter (grammar-owed: Iter<T>, keys — ADR-01)
		{module: "std/iter", export: "Iter", defKind: rast.DefType, catKind: "type"},
		{module: "std/iter", export: "keys", defKind: rast.DefNative, catKind: "function", native: cek.StdKeys},
		// std/taak (all, race, signal, sleep signatures)
		{module: "std/taak", export: "all", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		{module: "std/taak", export: "race", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		{module: "std/taak", export: "signal", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		{module: "std/taak", export: "sleep", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		// std/contract (requires, ensures) — purity enforced by V4 (Stage B)
		{module: "std/contract", export: "requires", defKind: rast.DefNative, catKind: "function", native: cek.StdContractRequires},
		{module: "std/contract", export: "ensures", defKind: rast.DefNative, catKind: "function", native: cek.StdContractEnsures},
		// std/mail (send — capability "mail.send", the V1 fixture target)
		{module: "std/mail", export: "send", defKind: rast.DefNative, catKind: "function", native: cek.StdMailSend, capability: "mail.send"},
	}

	im := &Image{
		ByHash:           map[string]*Entry{},
		CapabilityByHash: map[string]string{},
		ModuleStubs:      moduleStubs(),
		Epoch:            1,
	}
	for _, r := range roster {
		intrinsic := r.module + "." + r.export
		var body *rast.Node
		var typeNames []string
		if r.defKind == rast.DefType {
			// A single-parameter type alias whose body is `unknown` (the real shape
			// lives in the L0 stub). Iter<T> = unknown.
			body = typeAlias(tKeyword("unknown"))
			typeNames = []string{"T"}
		} else {
			body = nativeBody(intrinsic, tKeyword("unknown"))
		}
		norm := rast.Normalize(body)
		hash := rast.Address(norm)
		canon := rast.PrintModule(rast.PrintInput{
			Body:      norm,
			Name:      r.export,
			Exported:  true,
			Kind:      r.defKind,
			TypeNames: typeNames,
		})
		e := &Entry{
			CatalogName:   r.module + "/" + r.export,
			Module:        r.module,
			Export:        r.export,
			DefKind:       r.defKind,
			CatalogKind:   r.catKind,
			Body:          norm,
			Hash:          hash,
			CanonicalText: canon,
			Intrinsic:     intrinsic,
			Native:        r.native,
			Capability:    r.capability,
		}
		im.Entries = append(im.Entries, e)
		im.ByHash[hash] = e
		if r.capability != "" {
			im.CapabilityByHash[hash] = r.capability
		}
	}

	im.ManifestRoot = manifestRoot(im.Entries)
	im.Attestation = dispatchAttestation(im.Entries)
	return im
}

// --- rast node builders ------------------------------------------------------

func noneNode() *rast.Node    { return &rast.Node{Kind: rast.KNone} }
func klist(kids ...*rast.Node) *rast.Node {
	return &rast.Node{Kind: rast.KList, Kids: kids}
}
func tKeyword(s string) *rast.Node { return &rast.Node{Kind: rast.TKeyword, Str: s} }

// nativeBody builds a KNativeBody node (ADR-10 §1): Str = intrinsic symbol,
// Kids = [signature type]. The ADR-01 lowering has no production for this kind,
// so it is structurally unwritable through the live gate.
func nativeBody(intrinsic string, typ *rast.Node) *rast.Node {
	return &rast.Node{Kind: rast.KNativeBody, Str: intrinsic, Kids: []*rast.Node{typ}}
}

// typeAlias builds a KTypeAlias with a single type parameter: Kids = [KList of
// TParam, body]. The TParam name is out of the hash (schema.go), so it is empty
// here and supplied to the printer via TypeNames.
func typeAlias(body *rast.Node) *rast.Node {
	tparam := &rast.Node{Kind: rast.TParam, Kids: []*rast.Node{noneNode(), noneNode()}}
	return &rast.Node{Kind: rast.KTypeAlias, Kids: []*rast.Node{klist(tparam), body}}
}

// --- roots -------------------------------------------------------------------

// manifestRoot is SHA-256 over the sorted (name, hash) pairs (STAGE-A pin #10).
func manifestRoot(entries []*Entry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.CatalogName+"="+e.Hash)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// dispatchAttestation is H_dispatch (ADR-10 §2): SHA-256 over the sorted
// (intrinsic name, definition hash, native name) triples of every native.
// STAGE-A RESIDUE: the "Go body hash" is substituted by the intrinsic symbol
// (a stable identifier of the native fn); a future epoch hashes the compiled
// Go body directly.
func dispatchAttestation(entries []*Entry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Native == nil {
			continue
		}
		lines = append(lines, e.Intrinsic+"\t"+e.Hash+"\t"+e.Intrinsic)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// moduleStubs is the L0 std type surface (ADR-07 §2): signature-only per-module
// declarations served to tsgo at "/std/<mod>.ts". Hand-authored (see RESIDUE at
// the top of this file); the app imports resolve here through the tsx path map.
func moduleStubs() map[string]string {
	return map[string]string{
		"/std/iter.ts": "export type Iter<T> = { value: T; done: boolean };\n" +
			"export declare const keys: (obj: unknown) => string[];\n",
		"/std/taak.ts": "export declare const all: (xs: unknown[]) => unknown;\n" +
			"export declare const race: (xs: unknown[]) => unknown;\n" +
			"export declare const signal: (cls: string, restarts: unknown) => unknown;\n" +
			"export declare const sleep: (ms: number) => void;\n",
		"/std/contract.ts": "export declare const requires: (cond: boolean) => boolean;\n" +
			"export declare const ensures: (cond: boolean) => boolean;\n",
		"/std/mail.ts": "export declare const send: (to: string, subject: string) => " +
			"{ intent: string; to: string; subject: string };\n",
	}
}
