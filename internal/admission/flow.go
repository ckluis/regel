package admission

import (
	"fmt"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/rast"
)

// flow.go is the typed live-variable / taint dataflow the ADR-07 §4 V2 (pii-flow)
// and V5 (capture) verifiers share. Both walk the lowered rast body with the same
// De Bruijn binder-scope discipline the ADR-02 printer uses (params pushed on
// function entry, const/let binders pushed after their initializers, popped at
// block end), so a KLocal's index resolves to the binder it named.
//
// STAGE-C RESIDUE: the walk covers the straight-line + block/if subset the
// reference corpus exercises (function params, const/let, return, expr, if, the
// core expression forms). Destructuring patterns, for-of/while/switch/try binder
// scopes, and DefValue arrow bodies are conservatively skipped — a Stage-D
// widening, tracked exactly as V1's "dep-edge proxy" residue is. The analyses are
// SOUND for the forms they cover (they never miss a flow they model).

// binder is one value binder in scope, with the classifications V2 and V5 need.
type binder struct {
	id     int
	name   string
	pii    bool // V2: holds a vault/pii-typed value
	nonSer bool // V5: holds a live host resource (no encodable tag)
}

// defInfo is the per-definition classification context, computed once from the
// def's own imports so type references resolve without the (colliding) global
// type hashes.
type defInfo struct {
	piiTypeHashes  map[string]bool // hashes of this def's `Vault` (std/pii) imports
	connTypeHashes map[string]bool // hashes of this def's `Conn`  (std/sql) imports
	nameOf         map[*rast.Node]string
}

// newDefInfo builds the per-def type-hash sets from the def's dep edges. Every
// std TYPE shares the opaque genesis body (so their hashes collide); keying on
// the dep (module,name) disambiguates within a def that imports only one of them.
func newDefInfo(d lower.Definition) defInfo {
	di := defInfo{
		piiTypeHashes:  map[string]bool{},
		connTypeHashes: map[string]bool{},
		nameOf:         map[*rast.Node]string{},
	}
	for _, dep := range d.Deps {
		switch {
		case dep.Module == "std/pii" && dep.Name == "Vault":
			di.piiTypeHashes[dep.Hash] = true
		case dep.Module == "std/sql" && dep.Name == "Conn":
			di.connTypeHashes[dep.Hash] = true
		}
	}
	// Assign binder display names in the ADR-02 pre-order DFS (matches the printer),
	// so diagnostics can name the offending binding.
	vc := 0
	var walk func(*rast.Node)
	walk = func(n *rast.Node) {
		if n == nil {
			return
		}
		if n.Kind == rast.KBindId {
			nm := ""
			if vc < len(d.DisplayNames) {
				nm = d.DisplayNames[vc]
			}
			if nm == "" {
				nm = fmt.Sprintf("_v%d", vc)
			}
			di.nameOf[n] = nm
			vc++
		}
		for _, c := range n.Kids {
			walk(c)
		}
	}
	walk(d.Body)
	return di
}

func (di defInfo) isPiiType(typ *rast.Node) bool {
	return typ != nil && typ.Kind == rast.TCatRef && di.piiTypeHashes[typ.Str]
}
func (di defInfo) isConnType(typ *rast.Node) bool {
	return typ != nil && typ.Kind == rast.TCatRef && di.connTypeHashes[typ.Str]
}

// funcParts returns (params, retType, body) of a function-bodied definition.
func funcParts(d lower.Definition) (params []*rast.Node, retType, body *rast.Node, ok bool) {
	if d.Kind != rast.DefFunc || d.Body == nil || d.Body.Kind != rast.KFunc || len(d.Body.Kids) < 4 {
		return nil, nil, nil, false
	}
	f := d.Body
	if f.Kids[0] != nil {
		params = f.Kids[0].Kids
	}
	return params, f.Kids[2], f.Kids[3], true
}

// collectBindIds appends every KBindId leaf under a pattern (pre-order).
func collectBindIds(pat *rast.Node, out *[]*rast.Node) {
	if pat == nil {
		return
	}
	if pat.Kind == rast.KBindId {
		*out = append(*out, pat)
		return
	}
	for _, c := range pat.Kids {
		collectBindIds(c, out)
	}
}

func isLiteralNode(n *rast.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case rast.KNum, rast.KBigInt, rast.KStr, rast.KBool, rast.KRegex, rast.KTemplate:
		return true
	}
	return false
}

// stdIntrinsicOf returns the std intrinsic a callee KRef resolves to, or "".
func stdIntrinsicOf(callee *rast.Node, im *Image) string {
	if callee == nil || callee.Kind != rast.KRef {
		return ""
	}
	if e := im.ByHash[callee.Str]; e != nil {
		return e.Intrinsic
	}
	return ""
}

// ---------------------------------------------------------------------------
// V2 pii-flow taint analysis
// ---------------------------------------------------------------------------

type v2walk struct {
	im        *Image
	di        defInfo
	exported  bool
	defHash   string
	catName   string
	piiReturn map[string]bool // app-def hashes whose declared return type is pii
	env       []*binder
	nextID    int
	diags     []Diagnostic
	touched   map[string]bool // pii values reaching a sink (for the delta)
}

func (w *v2walk) push(name string, pii bool) {
	w.env = append(w.env, &binder{id: w.nextID, name: name, pii: pii})
	w.nextID++
}
func (w *v2walk) lookup(index uint64) *binder {
	i := len(w.env) - 1 - int(index)
	if i < 0 || i >= len(w.env) {
		return nil
	}
	return w.env[i]
}

// tainted reports whether an expression evaluates to a vault/pii value.
func (w *v2walk) tainted(n *rast.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case rast.KLocal:
		b := w.lookup(n.U)
		return b != nil && b.pii
	case rast.KCall:
		return w.taintedCall(n)
	case rast.KMember, rast.KIndex:
		return len(n.Kids) > 0 && w.tainted(n.Kids[0]) // reading a field of pii stays pii
	case rast.KAwait, rast.KAsConst, rast.KSatisfy, rast.KUnary:
		return len(n.Kids) > 0 && w.tainted(n.Kids[0])
	case rast.KBinary, rast.KCond, rast.KTemplate, rast.KObject, rast.KArray:
		for _, k := range flatKids(n) {
			if w.tainted(k) {
				return true
			}
		}
	}
	return false
}

// taintedCall classifies a call result: mask()/reveal() sanitize (clean); a pii-
// returning app helper taints; otherwise it propagates argument taint.
func (w *v2walk) taintedCall(call *rast.Node) bool {
	if len(call.Kids) < 2 {
		return false
	}
	callee := call.Kids[0]
	switch stdIntrinsicOf(callee, w.im) {
	case "std/pii.mask", "std/pii.reveal":
		return false // sanitizer
	}
	if callee != nil && callee.Kind == rast.KRef && w.piiReturn[callee.Str] {
		return true // helper whose declared return type is pii
	}
	// Propagate: a call over a tainted argument stays tainted (conservative).
	if call.Kids[1] != nil {
		for _, a := range call.Kids[1].Kids {
			if w.tainted(a) {
				return true
			}
		}
	}
	return false
}

// flatKids returns the expression children of a node, flattening KList wrappers
// (KObject/KArray hold a KList; KProp holds [key,value]).
func flatKids(n *rast.Node) []*rast.Node {
	var out []*rast.Node
	switch n.Kind {
	case rast.KObject:
		for _, p := range objProps(n) {
			if len(p.Kids) >= 2 {
				out = append(out, p.Kids[1])
			}
		}
	case rast.KArray:
		if len(n.Kids) > 0 && n.Kids[0] != nil {
			out = append(out, n.Kids[0].Kids...)
		}
	case rast.KTemplate:
		if len(n.Kids) > 0 && n.Kids[0] != nil {
			out = append(out, n.Kids[0].Kids...)
		}
	default:
		out = append(out, n.Kids...)
	}
	return out
}

// sinkCapArgs flags a capability-bearing std call carrying a tainted argument
// (an outbound / log boundary sink).
func (w *v2walk) checkExpr(n *rast.Node) {
	if n == nil {
		return
	}
	if n.Kind == rast.KCall && len(n.Kids) >= 2 {
		callee := n.Kids[0]
		if callee != nil && callee.Kind == rast.KRef {
			if _, capBearing := w.im.CapabilityByHash[callee.Str]; capBearing {
				if n.Kids[1] != nil {
					for _, a := range n.Kids[1].Kids {
						w.collectPiiRefs(a) // a pii value at this sink (masked or not)
						if w.tainted(a) {
							w.report("PII_ESCAPE", "a vault value flows unmasked into the capability sink "+
								stdIntrinsicOf(callee, w.im)+"; mask() or take a reveal-grant first")
						}
					}
				}
			}
		}
	}
	for _, k := range n.Kids {
		w.checkExpr(k)
	}
}

func (w *v2walk) report(code, msg string) {
	w.diags = append(w.diags, Diagnostic{
		StageOrVerifier: "V2", Code: code, Severity: "error",
		Subject: w.catName, Loc: Loc{DefHash: w.defHash}, Message: msg,
		Fix: "mask() the value at the boundary, take a reveal-grant, or route it through the vault; never sink or immortalize a vault value",
	})
}

// walkStmts processes a statement list with block scope (binders popped at end).
func (w *v2walk) walkStmts(stmts []*rast.Node) {
	base := len(w.env)
	for _, st := range stmts {
		w.walkStmt(st)
	}
	w.env = w.env[:base]
}

func (w *v2walk) walkStmt(st *rast.Node) {
	if st == nil {
		return
	}
	switch st.Kind {
	case rast.KVarDecl:
		if len(st.Kids) == 0 || st.Kids[0] == nil {
			return
		}
		for _, d := range st.Kids[0].Kids {
			if d == nil || d.Kind != rast.KDeclr || len(d.Kids) < 3 {
				continue
			}
			pat, typ, init := d.Kids[0], d.Kids[1], d.Kids[2]
			// PII_LITERAL: a literal given vault type — must never be immortalized.
			if w.di.isPiiType(typ) && isLiteralNode(init) {
				w.report("PII_LITERAL", "a literal is given vault type here — a PII literal must never be immortalized in the content store")
			}
			w.checkExpr(init)
			pii := w.di.isPiiType(typ) || w.tainted(init)
			var ids []*rast.Node
			collectBindIds(pat, &ids)
			for _, b := range ids {
				w.push(w.di.nameOf[b], pii)
			}
		}
	case rast.KReturn:
		if len(st.Kids) > 0 && !st.Kids[0].IsNone() {
			w.checkExpr(st.Kids[0])
			if w.exported {
				w.collectPiiRefs(st.Kids[0]) // pii reaching the boundary (masked or not)
				if w.tainted(st.Kids[0]) {
					w.report("PII_ESCAPE", fmt.Sprintf("the served definition %q returns a vault value (%s) unmasked at the response boundary",
						w.catName, w.returnedName(st.Kids[0])))
				}
			}
		}
	case rast.KExprStmt:
		if len(st.Kids) > 0 {
			w.checkExpr(st.Kids[0])
		}
	case rast.KIf:
		if len(st.Kids) >= 2 {
			w.checkExpr(st.Kids[0])
			w.walkStmt(st.Kids[1])
			if len(st.Kids) >= 3 && !st.Kids[2].IsNone() {
				w.walkStmt(st.Kids[2])
			}
		}
	case rast.KBlock:
		if len(st.Kids) > 0 && st.Kids[0] != nil {
			w.walkStmts(st.Kids[0].Kids)
		}
	}
}

// collectPiiRefs records the names of every pii binder referenced in a sink
// expression — through mask()/reveal() too — so the blast-radius delta reports a
// pii field "reaching a sink" whether or not it was masked there.
func (w *v2walk) collectPiiRefs(n *rast.Node) {
	if n == nil {
		return
	}
	if n.Kind == rast.KLocal {
		if b := w.lookup(n.U); b != nil && b.pii && b.name != "" {
			w.touched[b.name] = true
		}
	}
	for _, k := range n.Kids {
		w.collectPiiRefs(k)
	}
}

// returnedName names the returned binding for the diagnostic/delta, if it is a
// simple local reference; else "value".
func (w *v2walk) returnedName(n *rast.Node) string {
	if n != nil && n.Kind == rast.KLocal {
		if b := w.lookup(n.U); b != nil && b.name != "" {
			return b.name
		}
	}
	return "value"
}

// verifyV2Def runs V2 over one definition, returning its diagnostics and the set
// of pii values that reached a sink (for the blast-radius delta).
func verifyV2Def(ld loweredDef, im *Image, piiReturn map[string]bool) ([]Diagnostic, map[string]bool) {
	params, _, body, ok := funcParts(ld.Def)
	if !ok {
		return nil, nil
	}
	di := newDefInfo(ld.Def)
	w := &v2walk{
		im: im, di: di, exported: ld.Def.Exported,
		defHash: ld.Def.Hash, catName: ld.CatalogName,
		piiReturn: piiReturn, touched: map[string]bool{},
	}
	for _, prm := range params {
		if prm == nil || prm.Kind != rast.KParam || len(prm.Kids) < 2 {
			continue
		}
		pii := w.di.isPiiType(prm.Kids[1])
		var ids []*rast.Node
		collectBindIds(prm.Kids[0], &ids)
		for _, b := range ids {
			w.push(w.di.nameOf[b], pii)
		}
	}
	if body != nil && body.Kind == rast.KBlock && len(body.Kids) > 0 && body.Kids[0] != nil {
		w.walkStmts(body.Kids[0].Kids)
	}
	return w.diags, w.touched
}

// collectPiiTouched is the pii-surface projection for the blast-radius delta: the
// set of pii values that reach a boundary sink across the patch (masked or not).
func collectPiiTouched(lowered []loweredDef, im *Image) map[string]bool {
	piiReturn := piiReturnMap(lowered)
	out := map[string]bool{}
	for _, ld := range lowered {
		_, touched := verifyV2Def(ld, im, piiReturn)
		for name := range touched {
			out[name] = true
		}
	}
	return out
}

// piiReturnMap flags every in-patch definition whose declared return type is a
// vault/pii type (so a call to it taints — the multi-hop-through-helper case).
func piiReturnMap(lowered []loweredDef) map[string]bool {
	out := map[string]bool{}
	for _, ld := range lowered {
		_, ret, _, ok := funcParts(ld.Def)
		if !ok {
			continue
		}
		di := newDefInfo(ld.Def)
		if di.isPiiType(ret) {
			out[ld.Def.Hash] = true
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// V5 capture: live host resource across an await
// ---------------------------------------------------------------------------

type v5walk struct {
	im      *Image
	di      defInfo
	defHash string
	catName string
	env     []*binder
	nextID  int
	atRisk  map[int]bool // nonSer binders live at some earlier await
	diags   []Diagnostic
}

func (w *v5walk) push(name string, nonSer bool) {
	w.env = append(w.env, &binder{id: w.nextID, name: name, nonSer: nonSer})
	w.nextID++
}
func (w *v5walk) lookup(index uint64) *binder {
	i := len(w.env) - 1 - int(index)
	if i < 0 || i >= len(w.env) {
		return nil
	}
	return w.env[i]
}

// nonSerInit reports whether an initializer yields a live host resource.
func (w *v5walk) nonSerInit(init *rast.Node) bool {
	if init == nil {
		return false
	}
	if init.Kind == rast.KCall && len(init.Kids) >= 1 {
		callee := init.Kids[0]
		if callee != nil && callee.Kind == rast.KRef && w.im.NonSerialByHash[callee.Str] {
			return true
		}
	}
	return false
}

// containsAwait reports whether any await appears under n.
func containsAwait(n *rast.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == rast.KAwait {
		return true
	}
	for _, k := range n.Kids {
		if containsAwait(k) {
			return true
		}
	}
	return false
}

// refsAtRisk reports and records any reference to an at-risk (live-across-await)
// non-serializable binder in an expression.
func (w *v5walk) refsAtRisk(n *rast.Node) {
	if n == nil {
		return
	}
	if n.Kind == rast.KLocal {
		if b := w.lookup(n.U); b != nil && b.nonSer && w.atRisk[b.id] {
			// classify tag: a host resource has NO encodable tag (a value one past
			// the codec ceiling), so it is refused against the shared lattice.
			if !cfr.EncodableTags()[hostResourceTag()] {
				w.diags = append(w.diags, Diagnostic{
					StageOrVerifier: "V5", Code: "CAPTURE_UNSERIALIZABLE", Severity: "error",
					Subject: w.catName, Loc: Loc{DefHash: w.defHash},
					Message: fmt.Sprintf("binding %q is a live host resource (no encodable value tag) live across an await in %q; "+
						"its type lies outside the R2 serializable lattice", b.name, w.catName),
					Fix: "do not hold a connection/socket across an await — reacquire it after the await, or pass only serializable values across the checkpoint",
				})
			}
		}
	}
	for _, k := range n.Kids {
		w.refsAtRisk(k)
	}
}

// snapshotAtRisk marks every in-scope non-serializable binder as live across the
// await just crossed.
func (w *v5walk) snapshotAtRisk() {
	for _, b := range w.env {
		if b.nonSer {
			w.atRisk[b.id] = true
		}
	}
}

func (w *v5walk) walkStmts(stmts []*rast.Node) {
	base := len(w.env)
	for _, st := range stmts {
		w.walkStmt(st)
	}
	w.env = w.env[:base]
}

func (w *v5walk) walkStmt(st *rast.Node) {
	if st == nil {
		return
	}
	switch st.Kind {
	case rast.KVarDecl:
		if len(st.Kids) == 0 || st.Kids[0] == nil {
			return
		}
		for _, d := range st.Kids[0].Kids {
			if d == nil || d.Kind != rast.KDeclr || len(d.Kids) < 3 {
				continue
			}
			pat, typ, init := d.Kids[0], d.Kids[1], d.Kids[2]
			w.refsAtRisk(init)
			nonSer := w.di.isConnType(typ) || w.nonSerInit(init)
			if containsAwait(init) {
				w.snapshotAtRisk()
			}
			var ids []*rast.Node
			collectBindIds(pat, &ids)
			for _, b := range ids {
				w.push(w.di.nameOf[b], nonSer)
			}
		}
	case rast.KReturn:
		if len(st.Kids) > 0 && !st.Kids[0].IsNone() {
			w.refsAtRisk(st.Kids[0])
			if containsAwait(st.Kids[0]) {
				w.snapshotAtRisk()
			}
		}
	case rast.KExprStmt:
		if len(st.Kids) > 0 {
			w.refsAtRisk(st.Kids[0])
			if containsAwait(st.Kids[0]) {
				w.snapshotAtRisk()
			}
		}
	case rast.KIf:
		if len(st.Kids) >= 2 {
			w.refsAtRisk(st.Kids[0])
			if containsAwait(st.Kids[0]) {
				w.snapshotAtRisk()
			}
			w.walkStmt(st.Kids[1])
			if len(st.Kids) >= 3 && !st.Kids[2].IsNone() {
				w.walkStmt(st.Kids[2])
			}
		}
	case rast.KBlock:
		if len(st.Kids) > 0 && st.Kids[0] != nil {
			w.walkStmts(st.Kids[0].Kids)
		}
	}
}

// hostResourceTag is the sentinel "no encodable tag" a live host resource maps to
// — one past the codec ceiling, so it is never in cfr.EncodableTags(). Routing V5
// through the shared set (rather than a local boolean) is what makes a codec that
// GAINS a tag for such a resource automatically admit it, and a codec that LOSES a
// tag automatically narrow what V5 admits.
func hostResourceTag() cek.Tag { return cek.Tag(0xFF) }
