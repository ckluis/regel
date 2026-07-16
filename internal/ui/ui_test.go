package ui

import (
	"math/rand"
	"strings"
	"testing"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/rast"
)

// --- digest (ADR-11 §4): incremental == full, O(changed slots) ---------------

func TestDigestIncrementalEqualsFull(t *testing.T) {
	snap := map[string]string{}
	for i := 0; i < 64; i++ {
		snap[slotID(i)] = "v0"
	}
	d := FullDigest(snap)

	rng := rand.New(rand.NewSource(42))
	// Arbitrary edit sequence, INCLUDING mid-sequence re-edits of the same slot
	// (the exact case a position-ordered running hash cannot update in place).
	for step := 0; step < 500; step++ {
		id := slotID(rng.Intn(64))
		old := snap[id]
		nv := "v" + itoa(rng.Intn(1000))
		d = d.Set(id, old, nv)
		snap[id] = nv
		if step%37 == 0 {
			if got, want := d, FullDigest(snap); got != want {
				t.Fatalf("step %d: incremental=%d full=%d", step, got, want)
			}
		}
	}
	if got, want := d, FullDigest(snap); got != want {
		t.Fatalf("final: incremental=%d full=%d", got, want)
	}
}

// TestDigestAddRemove exercises the spliceList add/remove digest folds.
func TestDigestAddRemove(t *testing.T) {
	snap := map[string]string{"a": "1", "b": "2"}
	d := FullDigest(snap)
	d = d.Add("c", "3")
	snap["c"] = "3"
	if d != FullDigest(snap) {
		t.Fatal("add fold != full")
	}
	d = d.Remove("a", "1")
	delete(snap, "a")
	if d != FullDigest(snap) {
		t.Fatal("remove fold != full")
	}
}

// The microbench: a 2000-slot view editing one slot pays a ONE-slot digest update,
// not a 2000-term pass. BenchmarkDigestOneEdit reports ns/op independent of view
// size; this test proves correctness at that scale.
func TestDigestOneEditAtScale(t *testing.T) {
	const N = 2000
	snap := make(map[string]string, N)
	for i := 0; i < N; i++ {
		snap[slotID(i)] = "x"
	}
	d := FullDigest(snap)
	d = d.Set(slotID(1000), "x", "y")
	snap[slotID(1000)] = "y"
	if d != FullDigest(snap) {
		t.Fatal("one-edit incremental digest != full recompute at 2000 slots")
	}
}

func BenchmarkDigestOneEdit(b *testing.B) {
	const N = 2000
	snap := make(map[string]string, N)
	for i := 0; i < N; i++ {
		snap[slotID(i)] = "x"
	}
	d := FullDigest(snap)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d = d.Set(slotID(1000), "x", "y")
		d = d.Set(slotID(1000), "y", "x") // restore; two O(1) folds per iter
	}
	_ = d
}

func BenchmarkDigestFullRecompute(b *testing.B) {
	const N = 2000
	snap := make(map[string]string, N)
	for i := 0; i < N; i++ {
		snap[slotID(i)] = "x"
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FullDigest(snap)
	}
}

// --- codec (ADR-11 §2): round-trip all four op kinds + version -----------------

func TestCodecRoundTrip(t *testing.T) {
	f := Frame{
		EventSeq:     0xDEADBEEF,
		SnapshotHash: 0x0123456789ABCDEF,
		Ops: []Op{
			{SlotID: "detail.0", Kind: OpSetText, Payload: "hello <b>&"},
			{SlotID: "form.2", Kind: OpSetValue, Payload: "typed"},
			{SlotID: "badge.1", Kind: OpSetAttr, Attr: "data-state", Payload: "won"},
			{SlotID: "table.body", Kind: OpSpliceList, Splices: []Splice{
				{Kind: SpliceRemove, Key: "r7"},
				{Kind: SpliceAdd, Key: "r9", Index: 3, HTML: "<tr>…</tr>"},
				{Kind: SpliceMove, Key: "r2", Index: 0},
			}},
		},
	}
	b := EncodeFrame(f)
	if b[0] != CodecVersion {
		t.Fatalf("first byte %d, want version %d", b[0], CodecVersion)
	}
	got, err := DecodeFrame(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EventSeq != f.EventSeq || got.SnapshotHash != f.SnapshotHash {
		t.Fatalf("header mismatch: %+v", got)
	}
	if len(got.Ops) != len(f.Ops) {
		t.Fatalf("ops len %d want %d", len(got.Ops), len(f.Ops))
	}
	for i := range f.Ops {
		a, b := f.Ops[i], got.Ops[i]
		if a.SlotID != b.SlotID || a.Kind != b.Kind || a.Attr != b.Attr || a.Payload != b.Payload {
			t.Fatalf("op %d mismatch\n a=%+v\n b=%+v", i, a, b)
		}
		if len(a.Splices) != len(b.Splices) {
			t.Fatalf("op %d splice len", i)
		}
		for j := range a.Splices {
			if a.Splices[j] != b.Splices[j] {
				t.Fatalf("op %d splice %d: %+v vs %+v", i, j, a.Splices[j], b.Splices[j])
			}
		}
	}
}

func TestCodecRejectsBadInput(t *testing.T) {
	if _, err := DecodeFrame(nil); err == nil {
		t.Fatal("empty frame must error")
	}
	if _, err := DecodeFrame([]byte{99}); err == nil {
		t.Fatal("bad version must error")
	}
	good := EncodeFrame(Frame{EventSeq: 1, Ops: []Op{{SlotID: "a", Kind: OpSetText, Payload: "x"}}})
	if _, err := DecodeFrame(good[:len(good)-1]); err == nil {
		t.Fatal("truncated frame must error, not panic")
	}
}

// --- diff (ADR-11 §1): the diff unit is the slot ------------------------------

func detailTemplate() *Template {
	return &Template{
		Version: TemplateVersion, DefHash: "r1_x", Kind: "detail", Mount: "detail",
		Slots: []Slot{
			{ID: "detail.0", Kind: "setText", Field: "name"},
			{ID: "detail.1", Kind: "setText", Field: "score"},
			{ID: "detail.2", Kind: "setText", Field: "email", Masked: true, MaskLeaf: "text"},
		},
	}
}

func TestDiffSingleFieldChange(t *testing.T) {
	tpl := detailTemplate()
	last := map[string]Materialized{
		"detail.0": {Snapshot: "Acme", Display: "Acme"},
		"detail.1": {Snapshot: "10", Display: "10"},
		"detail.2": {Snapshot: "••••·abc123", Display: "••••·abc123"},
	}
	next := map[string]Materialized{
		"detail.0": {Snapshot: "Acme", Display: "Acme"},
		"detail.1": {Snapshot: "20", Display: "20"}, // only this changed
		"detail.2": {Snapshot: "••••·abc123", Display: "••••·abc123"},
	}
	ops := Diff(tpl, last, next)
	if len(ops) != 1 {
		t.Fatalf("want exactly 1 op, got %d: %+v", len(ops), ops)
	}
	if ops[0].SlotID != "detail.1" || ops[0].Kind != OpSetText || ops[0].Payload != "20" {
		t.Fatalf("wrong op: %+v", ops[0])
	}
}

func TestDiffListSplice(t *testing.T) {
	last := []ListRow{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	next := []ListRow{{Key: "c"}, {Key: "a"}, {Key: "d", HTML: "<tr>d</tr>"}} // b removed, d added, c moved
	op := DiffList("table.body", last, next)
	if op == nil {
		t.Fatal("expected a spliceList op")
	}
	var rm, add, mv int
	for _, s := range op.Splices {
		switch s.Kind {
		case SpliceRemove:
			if s.Key != "b" {
				t.Fatalf("removed wrong key %q", s.Key)
			}
			rm++
		case SpliceAdd:
			if s.Key != "d" || s.Index != 2 || s.HTML != "<tr>d</tr>" {
				t.Fatalf("bad add %+v", s)
			}
			add++
		case SpliceMove:
			mv++
		}
	}
	if rm != 1 || add != 1 || mv < 1 {
		t.Fatalf("splice counts rm=%d add=%d mv=%d", rm, add, mv)
	}
	// No structural change ⇒ nil op.
	if DiffList("table.body", last, last) != nil {
		t.Fatal("identical lists must produce no splice op")
	}
}

// --- render (ADR-11 §1/§8): escaping, ARIA, masking ---------------------------

func TestRenderFirstPaintEscapesAndAria(t *testing.T) {
	tpl := &Template{
		Version: TemplateVersion, DefHash: "r1_x", Kind: "detail", Mount: "detail",
		Root: &Node{Component: "section", Slot: -1, List: -1, Children: []*Node{
			{Component: "alert", Slot: -1, List: -1, Children: []*Node{
				{Component: "text", Slot: 0, List: -1},
			}},
		}},
		Slots: []Slot{{ID: "detail.0", Kind: "setText", Field: "name"}},
	}
	data := RenderData{Resource: "app/crm/Contact", Subject: "1", Fields: map[string]string{"name": "<script>&x"}}
	html, state := RenderFirstPaint(tpl, data, nil)
	if strings.Contains(html, "<script>") {
		t.Fatalf("unescaped markup leaked into HTML: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;&amp;x") {
		t.Fatalf("text not escaped: %s", html)
	}
	if !strings.Contains(html, `role="region"`) || !strings.Contains(html, `role="alert"`) || !strings.Contains(html, `aria-live="assertive"`) {
		t.Fatalf("expected ARIA landmarks/live region: %s", html)
	}
	if !strings.Contains(html, `data-slot="detail.0"`) {
		t.Fatalf("slot not addressable: %s", html)
	}
	if state["detail.0"].Display != "<script>&x" {
		t.Fatalf("snapshot display wrong: %+v", state["detail.0"])
	}
}

func TestRenderMaskingNoGrant(t *testing.T) {
	tpl := detailTemplate()
	tpl.Root = &Node{Component: "card", Slot: -1, List: -1, Children: []*Node{
		{Component: "text", Slot: 0, List: -1},
		{Component: "text", Slot: 1, List: -1},
		{Component: "text", Slot: 2, List: -1}, // masked email
	}}
	const secret = "ceo@acme.example"
	data := RenderData{Resource: "app/crm/Contact", Subject: "1", Fields: map[string]string{"name": "Acme", "score": "10"}}
	html, state := RenderFirstPaint(tpl, data, nil) // nil MaskCtx ⇒ no grant
	if strings.Contains(html, secret) {
		t.Fatal("plaintext must not appear with no grant")
	}
	m := state["detail.2"]
	if !strings.HasPrefix(m.Snapshot, MaskGlyph) || m.Display != m.Snapshot {
		t.Fatalf("masked slot must carry the mask token, got %+v", m)
	}
	if strings.Contains(m.Snapshot, secret) {
		t.Fatal("snapshot carries plaintext")
	}
}

func TestRenderMaskingWithGrant(t *testing.T) {
	tpl := detailTemplate()
	tpl.Root = &Node{Component: "card", Slot: -1, List: -1, Children: []*Node{
		{Component: "text", Slot: 2, List: -1},
	}}
	const secret = "founder@acme.example"
	audited := false
	mc := &MaskCtx{Principal: "human:dpo", Reveal: func(res, subj, field string) (string, string, bool) {
		if res == "app/crm/Contact" && subj == "9" && field == "email" {
			audited = true
			return secret, "grant-77", true
		}
		return "", "", false
	}}
	data := RenderData{Resource: "app/crm/Contact", Subject: "9", Fields: map[string]string{}}
	html, state := RenderFirstPaint(tpl, data, mc)
	if !audited {
		t.Fatal("reveal resolver (audit hook) was not called")
	}
	if !strings.Contains(html, secret) {
		t.Fatalf("granted plaintext must appear in the frame HTML: %s", html)
	}
	m := state["detail.2"]
	if strings.Contains(m.Snapshot, secret) {
		t.Fatal("INVARIANT VIOLATED: plaintext entered the slot snapshot")
	}
	if !strings.HasPrefix(m.Snapshot, MaskGlyph) || !strings.Contains(m.Snapshot, "grant-77") {
		t.Fatalf("revealed snapshot must be token|grantId, got %q", m.Snapshot)
	}
	if m.Display != secret {
		t.Fatalf("display must be plaintext under grant, got %q", m.Display)
	}
	// Expiry: resolver now denies ⇒ re-mask (snapshot changes, digest shifts).
	mc.Reveal = func(res, subj, field string) (string, string, bool) { return "", "", false }
	_, state2 := RenderFirstPaint(tpl, data, mc)
	if state2["detail.2"].Display == secret {
		t.Fatal("post-expiry render still shows plaintext")
	}
	if Diff(tpl, state, state2) == nil {
		t.Fatal("expiry must yield a re-mask diff")
	}
}

// --- template encoding round-trip --------------------------------------------

func TestTemplateEncodeDecode(t *testing.T) {
	tpl := detailTemplate()
	tpl.Root = &Node{Component: "card", Slot: -1, List: -1}
	b, err := tpl.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTemplate(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != TemplateVersion || got.DefHash != "r1_x" || len(got.Slots) != 3 {
		t.Fatalf("round-trip lost data: %+v", got)
	}
	if got.Slots[2].Field != "email" || !got.Slots[2].Masked {
		t.Fatalf("masked slot lost: %+v", got.Slots[2])
	}
}

// --- evalexpr (ADR-11 §1 hand-authored): prop-ref + field access --------------

func TestEvalSlotExprPropAndField(t *testing.T) {
	// props = { title: "Deal A", owner: { name: "Rae" } }
	inner := &cek.RecordObj{Keys: []string{"name"}, M: map[string]cek.Value{"name": cek.StrV("Rae")}}
	props := cek.Value{Tag: cek.TagRecord, Ref: &cek.RecordObj{
		Keys: []string{"title", "owner"},
		M: map[string]cek.Value{
			"title": cek.StrV("Deal A"),
			"owner": {Tag: cek.TagRecord, Ref: inner},
		},
	}}
	// props.title
	titleExpr := &rast.Node{Kind: rast.KMember, Str: "title", Kids: []*rast.Node{{Kind: rast.KLocal, U: 0}}}
	m := EvalSlotExpr(Slot{ID: "c.0", Kind: "setText"}, titleExpr, props, "", "", nil)
	if m.Display != "Deal A" {
		t.Fatalf("prop-ref field access = %q, want Deal A", m.Display)
	}
	// props.owner.name (member chain)
	nameExpr := &rast.Node{Kind: rast.KMember, Str: "name", Kids: []*rast.Node{
		{Kind: rast.KMember, Str: "owner", Kids: []*rast.Node{{Kind: rast.KLocal, U: 0}}},
	}}
	m = EvalSlotExpr(Slot{ID: "c.1", Kind: "setText"}, nameExpr, props, "", "", nil)
	if m.Display != "Rae" {
		t.Fatalf("nested field access = %q, want Rae", m.Display)
	}
}

// --- board grouping (BUILD-E D2) ---------------------------------------------

// boardTmpl builds a two-column board (states lead|won) grouping by "stage",
// each card a single text cell of the row's "name".
func boardTmpl() *Template {
	t := &Template{Version: TemplateVersion, DefHash: "h", Kind: "board", Mount: "board", GroupBy: "stage"}
	cols := []string{"lead", "won"}
	root := Static("grid")
	for j, st := range cols {
		listIdx := len(t.Slots)
		t.Slots = append(t.Slots, Slot{ID: "board.col" + itoa(j), Kind: "spliceList", Group: st})
		cellIdx := len(t.Slots)
		t.Slots = append(t.Slots, Slot{ID: "board.c" + itoa(j), Kind: "setText", Field: "name", Leaf: "text"})
		card := Static("card", Leaf("text", cellIdx))
		col := Static("section", Static("heading", Lit(st)), KeyedList("list", listIdx, card))
		root.Children = append(root.Children, col)
	}
	t.Root = root
	return t
}

// TestBoardFirstPaintGroups: first paint places each row's card under the column
// whose Group equals the row's stage, and nowhere else.
func TestBoardFirstPaintGroups(t *testing.T) {
	tmpl := boardTmpl()
	data := RenderData{Resource: "deals", Rows: []RowData{
		{Key: "1", Subject: "1", Fields: map[string]string{"name": "Acme", "stage": "lead"}},
		{Key: "2", Subject: "2", Fields: map[string]string{"name": "Beta", "stage": "won"}},
		{Key: "3", Subject: "3", Fields: map[string]string{"name": "Gamma", "stage": "lead"}},
	}}
	html, state := RenderFirstPaint(tmpl, data, nil)
	// Acme + Gamma render in the lead column cell (board.c0), Beta in the won cell.
	if got := state[RowSlotID("board.c0", "1")].Display; got != "Acme" {
		t.Fatalf("row 1 lead cell = %q, want Acme", got)
	}
	if got := state[RowSlotID("board.c0", "3")].Display; got != "Gamma" {
		t.Fatalf("row 3 lead cell = %q, want Gamma", got)
	}
	if got := state[RowSlotID("board.c1", "2")].Display; got != "Beta" {
		t.Fatalf("row 2 won cell = %q, want Beta", got)
	}
	// Beta must NOT appear in the lead column, nor Acme in the won column.
	if _, bad := state[RowSlotID("board.c0", "2")]; bad {
		t.Fatalf("won row leaked into the lead column")
	}
	if _, bad := state[RowSlotID("board.c1", "1")]; bad {
		t.Fatalf("lead row leaked into the won column")
	}
	// The HTML must place Acme before the won column header (grouping is real markup).
	if strings.Index(html, "Acme") > strings.Index(html, "Beta") {
		t.Fatalf("lead card must render before the won card in grouped HTML")
	}
}

// TestBoardMoveSplice: a row moving lead→won is a remove from the lead column list
// and an add to the won column list — the live kanban move (BUILD-E D2).
func TestBoardMoveSplice(t *testing.T) {
	tmpl := boardTmpl()
	// Before: row 1 is a lead. After: row 1 is won.
	before := []RowData{{Key: "1", Subject: "1", Fields: map[string]string{"name": "Acme", "stage": "lead"}}}
	after := []RowData{{Key: "1", Subject: "1", Fields: map[string]string{"name": "Acme", "stage": "won"}}}

	// Per-column key sequences before/after.
	leadKeys := func(rows []RowData) []ListRow {
		var out []ListRow
		for _, r := range rows {
			if r.Fields["stage"] == "lead" {
				out = append(out, ListRow{Key: r.Key})
			}
		}
		return out
	}
	wonRows := func(rows []RowData) []ListRow {
		var out []ListRow
		for _, r := range rows {
			if r.Fields["stage"] == "won" {
				h, _ := RenderRowForList(tmpl, "board.col1", "deals", r, nil)
				out = append(out, ListRow{Key: r.Key, HTML: h})
			}
		}
		return out
	}
	rmLead := DiffList("board.col0", leadKeys(before), leadKeys(after))
	if rmLead == nil || len(rmLead.Splices) != 1 || rmLead.Splices[0].Kind != SpliceRemove {
		t.Fatalf("lead column must lose row 1: %+v", rmLead)
	}
	addWon := DiffList("board.col1", wonRows(before), wonRows(after))
	if addWon == nil || len(addWon.Splices) != 1 || addWon.Splices[0].Kind != SpliceAdd {
		t.Fatalf("won column must gain row 1: %+v", addWon)
	}
	if !strings.Contains(addWon.Splices[0].HTML, "Acme") {
		t.Fatalf("added won card must carry the row's name: %q", addWon.Splices[0].HTML)
	}
}

// --- helpers -----------------------------------------------------------------

func slotID(i int) string { return "s" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
