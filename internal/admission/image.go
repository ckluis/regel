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
	Intrinsic     string       // "std/mail.send"
	Native        cek.NativeFn // nil for type-only entries
	Capability    string       // "" when the binding bears no capability
	// EffectClass is the ADR-10 §6 effect class a capability-bearing native
	// declares, verifier-visible: "read" (inline, no checkpoint), "write" (SQL +
	// checkpoint in one step transaction), "external" (an outbox intent, delivered
	// effectively-once), or "" for a pure/type binding. The metadata + lookup land
	// now (this increment); await-as-checkpoint ENFORCEMENT lands at D4.
	EffectClass string
	// NonSerial marks a binding or type whose value is a LIVE HOST RESOURCE — a
	// connection/socket that "is never a dialect value at all, so the codec has
	// no tag for it" (ADR-05 §3). A binding of such a type (or initialized by
	// such a native) live across an await is refused by V5 (CAPTURE_UNSERIALIZABLE).
	NonSerial bool
}

// Image is the whole compiled genesis image.
type Image struct {
	Entries           []*Entry
	ByHash            map[string]*Entry
	CapabilityByHash  map[string]string // capability-bearing std hashes → capability
	EffectClassByHash map[string]string // capability-bearing std hashes → effect class (ADR-10 §6)
	NonSerialByHash   map[string]bool   // host-resource type/native hashes (V5, ADR-05 §3)
	ModuleStubs       map[string]string // "/std/mail.ts" → L0 stub text
	ManifestRoot      string            // SHA-256 over sorted (name,hash)
	Attestation       string            // H_dispatch (ADR-10 §2)
	Epoch             int
}

var (
	imageOnce sync.Once
	imageInst *Image

	imageE2Once sync.Once
	imageE2Inst *Image
)

// BuildImage compiles (once) and returns the deterministic genesis image (epoch 1).
func BuildImage() *Image {
	imageOnce.Do(func() { imageInst = buildImage(1, nil, nil) })
	return imageInst
}

// stdTextDelta is the epoch-2 std/ delta (BUILD-F R9): a NEW std battery type
// `std/text.Slug`. It is TYPE-ONLY on purpose — a type adds no native, so it moves
// the std-manifest-root while leaving the dispatch attestation (H_dispatch, the
// "kernel binary" half of the epoch pair, ADR-08 §2) UNCHANGED. That isolates the
// std-manifest-root as the sole epoch discriminator, so an old-pair binary booting
// the new-pair catalog refuses with the canonical `manifest_root_mismatch` — the
// exact std-manifest-root fence R9 exercises. It is the ADR-08 §6 patch-epoch shape
// (a new manifest swapping/adding std hashes with the same binary).
func stdTextDelta() (rosterEntry, string, string) {
	return rosterEntry{module: "std/text", export: "Slug", defKind: rast.DefType, catKind: "type"},
		"/std/text.ts",
		"export type Slug = string;\n"
}

// BuildImageEpoch2 compiles (once) and returns the deterministic epoch-2 image:
// the epoch-1 roster PLUS the std/text.Slug delta, pinned at Epoch 2 with the
// resulting new std-manifest-root (attestation held constant). It is a real,
// genesis-able and migrate-target-able image — the second point of the ADR-08 §1
// two-point combination space, used by the R9 migrate-in-drill.
func BuildImageEpoch2() *Image {
	imageE2Once.Do(func() {
		re, stubPath, stubText := stdTextDelta()
		imageE2Inst = buildImage(2, []rosterEntry{re}, map[string]string{stubPath: stubText})
	})
	return imageE2Inst
}

// EffectClassOf returns the ADR-10 §6 effect class a std native declares
// ("read"/"write"/"external"), or "" for a pure/type binding or an unknown hash.
// The verifier-visible metadata lands now; await-as-checkpoint enforcement (D4)
// consumes this lookup.
func (im *Image) EffectClassOf(hash string) string { return im.EffectClassByHash[hash] }

// Registry builds a fresh native dispatch registry (hash → Go function) from the
// image — the kernel populates the interpreter with this at genesis/boot.
func (im *Image) Registry() *cek.Registry {
	reg := cek.NewRegistry()
	for _, e := range im.Entries {
		if e.Native != nil {
			reg.Register(e.Hash, e.Native)
			// BUILD-D D4: publish the declared effect class so the machine can enforce
			// the ADR-10 §6 std-conformance gate (a read-declared native that records
			// a write/external effect is caught and failed closed).
			reg.SetEffectClass(e.Hash, e.EffectClass)
		}
	}
	return reg
}

// nativeStub is the Stage-A native for std bindings not otherwise exercised
// (taak.all/race/signal/sleep): the dispatch bijection requires a registered
// implementation for every NativeBody hash. It records no effect and returns
// undefined. STAGE-A RESIDUE: real taak join/timer semantics are Stage B.
func nativeStub(_ *cek.Host, _ []cek.Value) (cek.Value, *cek.NativePark) {
	return cek.UndefV(), nil
}

// roster is the fixed Stage-A micro-std vocabulary.
type rosterEntry struct {
	module      string
	export      string
	defKind     rast.DefKind
	catKind     string
	native      cek.NativeFn
	capability  string
	effectClass string
	nonSerial   bool
}

func buildImage(epoch int, extra []rosterEntry, extraStubs map[string]string) *Image {
	roster := []rosterEntry{
		// std/iter (grammar-owed: Iter<T>, keys — ADR-01)
		{module: "std/iter", export: "Iter", defKind: rast.DefType, catKind: "type"},
		{module: "std/iter", export: "keys", defKind: rast.DefNative, catKind: "function", native: cek.StdKeys},
		// std/taak (BUILD-D D4, ADR-10 §6): the REAL v1 workflow-authoring surface.
		// The taak.* natives reuse the StdWf* machinery (one implementation, two
		// module names — the hashes differ because the NativeBody intrinsic symbol
		// differs, so both dispatch to the same Go body). signal writes the durable
		// condition + restart rows and parks manual (ADR-05 §6); onChange parks on an
		// event wake (ADR-05 §5). taak is the authoring surface; std/wf remains the
		// Stage-B alias so existing workflows keep resolving.
		{module: "std/taak", export: "sleep", defKind: rast.DefNative, catKind: "function", native: cek.StdWfSleep},
		{module: "std/taak", export: "receive", defKind: rast.DefNative, catKind: "function", native: cek.StdWfReceive},
		{module: "std/taak", export: "send", defKind: rast.DefNative, catKind: "function", native: cek.StdWfSend},
		{module: "std/taak", export: "all", defKind: rast.DefNative, catKind: "function", native: cek.StdWfAll},
		{module: "std/taak", export: "race", defKind: rast.DefNative, catKind: "function", native: cek.StdWfRace},
		{module: "std/taak", export: "signal", defKind: rast.DefNative, catKind: "function", native: cek.StdTaakSignal},
		{module: "std/taak", export: "onChange", defKind: rast.DefNative, catKind: "function", native: cek.StdTaakOnChange},
		// BUILD-E (D10, ADR-10 §6 / ADR-06 cron): schedule registers a recurring cron
		// task row the reactor's cron driver fires. Effect class write (it materializes
		// a durable cron row in the step transaction).
		{module: "std/taak", export: "schedule", defKind: rast.DefNative, catKind: "function", native: cek.StdTaakSchedule, effectClass: "write"},
		// std/contract (requires, ensures) — purity enforced by V4 (Stage B)
		{module: "std/contract", export: "requires", defKind: rast.DefNative, catKind: "function", native: cek.StdContractRequires},
		{module: "std/contract", export: "ensures", defKind: rast.DefNative, catKind: "function", native: cek.StdContractEnsures},
		// std/contract (BUILD-C, ADR-10 §3/§137): pre/post are the pre/postcondition
		// combinators attachable to a definition (ADR-02 §3 — contracts are subset
		// code in the body, mirrored to definition.contracts). V4 enforces they are
		// well-formed and PURE (a capability named in a clause ⇒ CONTRACT_EFFECTFUL;
		// a governance/out-of-scope symbol ⇒ CONTRACT_MALFORMED). BUILD-C (C4): the
		// natives ENFORCE at the eval boundary — a falsy clause is a typed durable
		// contract.{pre,post}.violated park (the runtime discharge of the V4-derived
		// validator artifacts; a pre violation fires no effect). Rebinding a native
		// leaves hashes/attestation untouched (both key on the intrinsic symbol).
		{module: "std/contract", export: "pre", defKind: rast.DefNative, catKind: "function", native: cek.StdContractPre},
		{module: "std/contract", export: "post", defKind: rast.DefNative, catKind: "function", native: cek.StdContractPost},
		// std/pii (BUILD-C, ADR-10 §4 item 5 / §5 modifier): Vault<T> is the pii /
		// vault-routed value type; mask()/reveal() are the masking + reveal-grant
		// combinators (the only sanitizers V2 pii-flow recognizes). A vault value
		// reaching a boundary sink unmasked ⇒ PII_ESCAPE; a vault-typed literal ⇒
		// PII_LITERAL (the immortality interaction).
		{module: "std/pii", export: "Vault", defKind: rast.DefType, catKind: "type"},
		{module: "std/pii", export: "mask", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		{module: "std/pii", export: "reveal", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		// std/sql (BUILD-C, ADR-10 §3 std/sql SHIP + §Red-Path "socket-typed value
		// live across await"): the MINIMAL host-resource slice the V5 capture fixture
		// needs. Conn is a live connection handle — a host resource with NO encodable
		// Value tag (ADR-05 §3), so a Conn live across an await is CAPTURE_UNSERIALIZABLE.
		// The full std/sql parameterized-query surface (ADR-10 §3) lands at Stage D.
		// Conn is detected as a host-resource TYPE by its (module,name) dep — every
		// std TYPE shares the opaque `unknown` genesis body (so their hashes collide;
		// the L0 stub carries the real shape), so type classification keys on the dep
		// name, never the hash. connect() is a value native with a unique hash, so its
		// non-serial result is keyed by hash (NonSerialByHash).
		{module: "std/sql", export: "Conn", defKind: rast.DefType, catKind: "type"},
		{module: "std/sql", export: "connect", defKind: rast.DefNative, catKind: "function", native: nativeStub, nonSerial: true},
		// BUILD-E (D1, ADR-10 §4): the typed parameterized-query surface. query is a
		// capability-gated (`sql.query`) SELECT-only read against the derived resource
		// tables — read-safe by construction (a non-SELECT fails closed at the native
		// boundary), effect class read (inline, no checkpoint), honoring the eval's
		// as-of read context. Row is the opaque row type the query returns.
		{module: "std/sql", export: "Row", defKind: rast.DefType, catKind: "type"},
		{module: "std/sql", export: "query", defKind: rast.DefNative, catKind: "function", native: cek.StdSQLQuery, capability: "sql.query", effectClass: "read"},
		// std/mail (send — capability "mail.send", the V1 fixture target; effect
		// class external per ADR-10 §6: the step transaction writes an outbox intent).
		{module: "std/mail", export: "send", defKind: rast.DefNative, catKind: "function", native: cek.StdMailSend, capability: "mail.send", effectClass: "external"},
		// std/policy (BUILD-C, ADR-10 §4 item 4): the governance policy vocabulary
		// the derivation wires into every read path — V3 catalog-parity's subject.
		// orgScoped is the product-scope org/role predicate; policy(name) declares a
		// named policy artifact. (std/pii mask/reveal + std/contract pre/post are C2.)
		{module: "std/policy", export: "orgScoped", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		{module: "std/policy", export: "policy", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		// std/resource (BUILD-C, ADR-10 §4): the erf resource(...) declaration
		// combinator — a declared field map (plain + pii-typed kinds) with optional
		// policy wiring, whose additive DDL V6 derivation-parity checks total.
		{module: "std/resource", export: "resource", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		// std/wf (Stage-B wake vocabulary — ADR-05 §5 BUILD-B)
		{module: "std/wf", export: "sleep", defKind: rast.DefNative, catKind: "function", native: cek.StdWfSleep},
		{module: "std/wf", export: "receive", defKind: rast.DefNative, catKind: "function", native: cek.StdWfReceive},
		{module: "std/wf", export: "send", defKind: rast.DefNative, catKind: "function", native: cek.StdWfSend},
		{module: "std/wf", export: "all", defKind: rast.DefNative, catKind: "function", native: cek.StdWfAll},
		{module: "std/wf", export: "race", defKind: rast.DefNative, catKind: "function", native: cek.StdWfRace},
	}
	// --- Stage-D std/ world completion (ADR-10 §3 SHIP roster, BUILD-D D0) -------
	// The remaining 14-battery SHIP roster, added MINIMAL-but-real. Later Stage-D
	// increments own erf DERIVATION (D3), rendering + reactive runtime (D1/D2), and
	// the real taak natives (D4/D5); D0 lands the ROSTER (rows, dispatch, L0 surface,
	// effect-class metadata) so admitted code can import and typecheck the world.
	roster = append(roster,
		// std/identity (ADR-10 §3 SHIP): orgs → users → roles → sessions → API keys
		// ship in core. Types + minimal read natives; the policy/audit machinery that
		// builds on it is later. currentUser/currentOrg are reads of session context.
		rosterEntry{module: "std/identity", export: "Org", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/identity", export: "User", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/identity", export: "Role", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/identity", export: "Session", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/identity", export: "ApiKey", defKind: rast.DefType, catKind: "type"},
		// BUILD-E (D6a): currentUser/currentOrg are now ROW-BACKED reads of the
		// evaluating principal's user_account row (ADR-10 §3), not stubs — the read
		// seam is cek.Reader (nil in unit tests ⇒ null, never a fake).
		rosterEntry{module: "std/identity", export: "currentUser", defKind: rast.DefNative, catKind: "function", native: cek.StdIdentityCurrentUser, effectClass: "read"},
		rosterEntry{module: "std/identity", export: "currentOrg", defKind: rast.DefNative, catKind: "function", native: cek.StdIdentityCurrentOrg, effectClass: "read"},
		// std/http (ADR-10 §3 SHIP minimal): outbound call is a CAPABILITY, effect
		// class external (V2 treats it as a sink; ADR-06 dispatcher delivers).
		rosterEntry{module: "std/http", export: "get", defKind: rast.DefNative, catKind: "function", native: cek.StdHTTPGet, capability: "http.get", effectClass: "external"},
		rosterEntry{module: "std/http", export: "post", defKind: rast.DefNative, catKind: "function", native: cek.StdHTTPPost, capability: "http.post", effectClass: "external"},
		// std/time (ADR-10 §3 SHIP): now() reads the clock inline (effect class read);
		// sleep already lives in std/taak (wakes are rows, ADR-05 §7).
		rosterEntry{module: "std/time", export: "now", defKind: rast.DefNative, catKind: "function", native: cek.StdTimeNow, effectClass: "read"},
		// std/money (ADR-10 §3/§5 SHIP): decimal money as a minor-units bigint +
		// currency record — NO float. money() constructs, format() renders.
		rosterEntry{module: "std/money", export: "Money", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/money", export: "money", defKind: rast.DefNative, catKind: "function", native: cek.StdMoney},
		rosterEntry{module: "std/money", export: "format", defKind: rast.DefNative, catKind: "function", native: cek.StdMoneyFormat},
		// std/crypto (ADR-10 §3 SHIP intrinsic-only): vetted AEAD over Go crypto; no
		// key material is a dialect value — the key is referenced by a token string.
		rosterEntry{module: "std/crypto", export: "aeadSeal", defKind: rast.DefNative, catKind: "function", native: cek.StdAeadSeal},
		rosterEntry{module: "std/crypto", export: "aeadOpen", defKind: rast.DefNative, catKind: "function", native: cek.StdAeadOpen},
		// std/test (ADR-10 §3 SHIP): fake registration, minimal — a fake is an
		// admitted row checked against the intrinsic's contracts (later).
		rosterEntry{module: "std/test", export: "fake", defKind: rast.DefNative, catKind: "function", native: nativeStub},
		// std/log (ADR-10 §3 SHIP tiny): write() is a sink, effect class external —
		// the log sink is in V2's sink set (a pii log line ⇒ V2 reject on the caller).
		rosterEntry{module: "std/log", export: "write", defKind: rast.DefNative, catKind: "function", native: cek.StdLogWrite, effectClass: "external"},
		// std/erf (ADR-10 §3 SHIP): read/list as STUBS for now — D3 implements the
		// erf read surface. Type-only Row + nativeStub reads (effect class read).
		rosterEntry{module: "std/erf", export: "Row", defKind: rast.DefType, catKind: "type"},
		rosterEntry{module: "std/erf", export: "read", defKind: rast.DefNative, catKind: "function", native: nativeStub, effectClass: "read"},
		rosterEntry{module: "std/erf", export: "list", defKind: rast.DefNative, catKind: "function", native: nativeStub, effectClass: "read"},
	)
	// std/ui: the closed 25 tier-1 semantic components (ADR-10 §7). Each is a native
	// constructor returning a plain {component, props, children} record over the
	// existing encodable tags, so CFR capture of UI state round-trips unchanged. The
	// six masking leaves are marked in cek.MaskingLeaves (D2 owns masking behavior).
	for _, name := range cek.UITier1 {
		roster = append(roster, rosterEntry{
			module: "std/ui", export: name, defKind: rast.DefNative,
			catKind: "function", native: cek.UINative(name),
		})
	}
	// Epoch-delta roster entries (BUILD-F R9): a later epoch's NEW std entries slot
	// through the same deterministic compile path, so the epoch-N image is built
	// exactly like genesis — no side engine (ADR-08 §1).
	roster = append(roster, extra...)

	stubs := moduleStubs()
	for path, text := range extraStubs {
		stubs[path] = text
	}
	im := &Image{
		ByHash:            map[string]*Entry{},
		CapabilityByHash:  map[string]string{},
		EffectClassByHash: map[string]string{},
		NonSerialByHash:   map[string]bool{},
		ModuleStubs:       stubs,
		Epoch:             epoch,
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
			EffectClass:   r.effectClass,
			NonSerial:     r.nonSerial,
		}
		im.Entries = append(im.Entries, e)
		im.ByHash[hash] = e
		if r.capability != "" {
			im.CapabilityByHash[hash] = r.capability
		}
		if r.effectClass != "" {
			im.EffectClassByHash[hash] = r.effectClass
		}
		if r.nonSerial {
			im.NonSerialByHash[hash] = true
		}
	}

	im.ManifestRoot = manifestRoot(im.Entries)
	im.Attestation = dispatchAttestation(im.Entries)
	return im
}

// --- rast node builders ------------------------------------------------------

func noneNode() *rast.Node { return &rast.Node{Kind: rast.KNone} }
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
		// std/taak L0 (BUILD-D D4, ADR-10 §6): the real workflow-authoring surface.
		// receive takes an optional {path, equals} match predicate; onChange parks on
		// a derived-resource change; signal writes a durable condition + restarts.
		"/std/taak.ts": "export type Match = { path: string; equals: unknown };\n" +
			"export type Restart = { name: string; label: string; capability?: string };\n" +
			"export declare const sleep: (ms: number) => void;\n" +
			"export declare const receive: <T>(channel: string, match?: Match) => T;\n" +
			"export declare const send: <T>(channel: string, value: T) => void;\n" +
			"export declare const all: <T>(thunks: (() => T)[]) => T[];\n" +
			"export declare const race: <T>(thunks: (() => T)[]) => T;\n" +
			"export declare const signal: (cls: string, restarts: Restart[], payload?: unknown) => { restart: string };\n" +
			"export declare const onChange: (resource: string, keys?: string[]) => void;\n" +
			"export declare const schedule: (schedule: string, target: string) => void;\n",
		"/std/contract.ts": "export declare const requires: (cond: boolean) => boolean;\n" +
			"export declare const ensures: (cond: boolean) => boolean;\n" +
			"export declare const pre: (cond: boolean) => void;\n" +
			"export declare const post: (cond: boolean) => void;\n",
		// std/pii L0 (BUILD-C, ADR-10 §4/§5). Vault<T> is the pii/vault-routed value
		// type; pii-ness travels through the type ANNOTATION (V2 reads it off the
		// lowered TCatRef), so the alias is transparent at the type level while the
		// nominal reference survives lowering. mask()/reveal() are the only sanitizers.
		"/std/pii.ts": "export type Vault<T> = T;\n" +
			"export declare const mask: <T>(v: Vault<T>) => string;\n" +
			"export declare const reveal: <T>(v: Vault<T>, grant: string) => T;\n",
		// std/sql L0 (BUILD-C, ADR-10 §3 minimal): Conn is a live host-resource handle.
		// BUILD-E (D1, ADR-10 §4): query is the typed parameterized SELECT-only surface.
		// A Row is an opaque record of string-keyed scalar columns; params bind as $1…$n
		// (no string SQL is interpolable — the read-safety guarantee).
		"/std/sql.ts": "export type Conn = { readonly __conn: string };\n" +
			"export type Row = { readonly [column: string]: string };\n" +
			"export declare const connect: () => Conn;\n" +
			"export declare const query: (conn: Conn, sql: string, params: (string | number)[]) => Row[];\n",
		"/std/mail.ts": "export declare const send: (to: string, subject: string) => " +
			"{ intent: string; to: string; subject: string };\n",
		// std/policy L0 (BUILD-C, ADR-10 §4). A Policy is an opaque governance
		// predicate; orgScoped is the built-in org/role scope, policy(name) declares
		// a named one. std/pii + std/contract land in C2 — room left, not built.
		"/std/policy.ts": "export type Policy = { readonly __policy: string };\n" +
			"export declare const orgScoped: Policy;\n" +
			"export declare const policy: (name: string) => Policy;\n",
		// std/resource L0 (BUILD-C, ADR-10 §4/§5). FieldSpec is the closed
		// field-type surface at MINIMAL Stage-C scope: plain base kinds plus the
		// pii(<base>) modifier over a subset — the full 13 base types land at Stage D
		// behind the same seam. A resource declares a field map and an optional policy.
		"/std/resource.ts": "import { Policy } from \"std/policy\";\n" +
			"export type Base =\n" +
			"  | \"text\" | \"longtext\" | \"number\" | \"money\" | \"boolean\"\n" +
			"  | \"date\" | \"timestamp\" | \"email\" | \"phone\" | \"url\" | \"address\";\n" +
			"export type FieldSpec =\n" +
			"  | Base\n" +
			"  | `select:${string}`\n" +
			"  | `states:${string}`\n" +
			"  | `belongsTo:${string}`\n" +
			"  | `hasMany:${string}`\n" +
			"  | `pii:${Base}`;\n" +
			"export type ResourceDecl = {\n" +
			"  fields: { readonly [name: string]: FieldSpec };\n" +
			"  policy?: Policy;\n" +
			"};\n" +
			"export type Resource = { readonly __resource: string };\n" +
			"export declare const resource: (decl: ResourceDecl) => Resource;\n",
		"/std/wf.ts": "export declare const sleep: (ms: number) => void;\n" +
			"export declare const receive: <T>(channel: string) => T;\n" +
			"export declare const send: <T>(channel: string, value: T) => void;\n" +
			"export declare const all: <T>(thunks: (() => T)[]) => T[];\n" +
			"export declare const race: <T>(thunks: (() => T)[]) => T;\n",
		// std/identity L0 (BUILD-D, ADR-10 §3; BUILD-E D6a). User/Org expose the
		// row-backed fields currentUser/currentOrg read from user_account, so admitted
		// CRM code can do real per-user logic (u.org, u.roles). The stub shape does not
		// affect the type hash (types share the opaque `unknown` genesis body), so
		// enriching it is not an epoch change. currentUser/currentOrg read the evaluating
		// principal (a runtime null for an unmapped principal is the documented edge).
		"/std/identity.ts": "export type Org = { readonly id: string; readonly name: string };\n" +
			"export type User = { readonly id: string; readonly org: string; readonly email: string; readonly name: string; readonly roles: string };\n" +
			"export type Role = { readonly __role: string };\n" +
			"export type Session = { readonly __session: string };\n" +
			"export type ApiKey = { readonly __apiKey: string };\n" +
			"export declare const currentUser: () => User;\n" +
			"export declare const currentOrg: () => Org;\n",
		// std/http L0 (BUILD-D, ADR-10 §3 minimal): outbound capability calls.
		"/std/http.ts": "export declare const get: (url: string) => { intent: string; url: string };\n" +
			"export declare const post: (url: string, body: string) => { intent: string; url: string };\n",
		// std/time L0 (BUILD-D, ADR-10 §3): now() reads the clock; sleep is std/taak.
		"/std/time.ts": "export declare const now: () => number;\n",
		// std/money L0 (BUILD-D, ADR-10 §5): decimal money — minor-units bigint +
		// currency, NO float. money() constructs, format() renders.
		"/std/money.ts": "export type Money = { readonly minorUnits: bigint; readonly currency: string };\n" +
			"export declare const money: (minorUnits: bigint, currency: string) => Money;\n" +
			"export declare const format: (m: Money) => string;\n",
		// std/crypto L0 (BUILD-D, ADR-10 §3 intrinsic-only): AEAD keyed by an opaque
		// token string — no key material is a dialect value.
		"/std/crypto.ts": "export declare const aeadSeal: (keyToken: string, plaintext: string) => string;\n" +
			"export declare const aeadOpen: (keyToken: string, ciphertext: string) => string;\n",
		// std/test L0 (BUILD-D, ADR-10 §3): fake registration, minimal.
		"/std/test.ts": "export declare const fake: (name: string, impl: unknown) => unknown;\n",
		// std/log L0 (BUILD-D, ADR-10 §3 tiny): write() is a sink (V2's sink set).
		"/std/log.ts": "export declare const write: (message: string) => void;\n",
		// std/erf L0 (BUILD-D, ADR-10 §3): read/list stubs — D3 implements the surface.
		"/std/erf.ts": "export type Row = { readonly __erf: string };\n" +
			"export declare const read: (resource: string, id: string) => Row;\n" +
			"export declare const list: (resource: string) => Row[];\n",
		// std/ui L0 (BUILD-D, ADR-10 §7): the closed 25 tier-1 components. Each is a
		// constructor over {component, props, children}; the roster is generated so
		// the stub can never drift from cek.UITier1.
		"/std/ui.ts": uiModuleStub(),
	}
}

// uiModuleStub builds the std/ui L0 surface from cek.UITier1 (ADR-10 §7): the
// UINode node shape plus one constructor declaration per tier-1 component, so the
// stub and the dispatch roster are generated from the SAME closed list.
func uiModuleStub() string {
	var b strings.Builder
	b.WriteString("export type UIProps = { readonly [k: string]: unknown };\n")
	b.WriteString("export type UINode = { readonly component: string; readonly props: UIProps; readonly children: UINode[] };\n")
	for _, name := range cek.UITier1 {
		b.WriteString("export declare const " + name +
			": (props?: UIProps, children?: UINode[]) => UINode;\n")
	}
	return b.String()
}
