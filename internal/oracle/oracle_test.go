// oracle_test.go is the regel-native differential oracle harness (ADR-04 §6
// harness 3; seated in the ADR-07 §5 release gate, R1-02). It drives every
// corpus case + input vector through BOTH the production CEK machine and the
// independent reference reducer and compares the four observables: (i)
// contract/validator verdicts (accept/reject + rejecting clause subject), (ii)
// validator outcomes per input, (iii) the effect-class order trace, (iv) the
// produced values. ANY divergence is red. The oracle is then validated against
// itself: each seeded wrong-evaluation mutant in the production evaluator
// (mutants.Evaluator — one per covered layer) MUST cause a caught divergence; a
// surviving mutant is a release blocker.
package oracle_test

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/oracle"
	"regel.dev/regel/internal/rast"
)

// loweredCase is one corpus case lowered to catalogable definitions.
type loweredCase struct {
	c         oracle.Case
	entryHash string
	defs      map[string]*rast.Node // hash → body (this module's defs)
}

// lowerCorpus lowers every corpus case against the genesis image's std world.
// The program under test is the SAME canonical AST for both evaluators; only
// the evaluators differ (ADR-04 §6: same program, same inputs, two witnesses).
func lowerCorpus(t *testing.T, im *admission.Image) []loweredCase {
	t.Helper()
	byName := map[string]string{} // catalog name → hash
	for _, e := range im.Entries {
		byName[e.CatalogName] = e.Hash
	}
	resolve := func(qualified string) (string, bool) {
		// "std/mail.send" → "std/mail/send" (the lowering resolver contract).
		name := qualified
		if i := strings.LastIndex(qualified, "."); i >= 0 {
			name = qualified[:i] + "/" + qualified[i+1:]
		}
		h, ok := byName[name]
		return h, ok
	}

	var out []loweredCase
	for _, c := range oracle.Corpus {
		res := lower.Module(c.Source, lower.ModuleContext{ModuleName: c.Module, Resolve: resolve})
		if !res.OK() {
			t.Fatalf("corpus case %s does not lower: %+v", c.Name, res.Diagnostics)
		}
		lc := loweredCase{c: c, defs: map[string]*rast.Node{}}
		for _, d := range res.Definitions {
			lc.defs[d.Hash] = d.Body
			if d.Name == c.Entry {
				lc.entryHash = d.Hash
			}
		}
		if lc.entryHash == "" {
			t.Fatalf("corpus case %s: entry %s not found", c.Name, c.Entry)
		}
		out = append(out, lc)
	}
	return out
}

// --- argument construction (each side builds its OWN values) -------------------

func cekArgs(in []any) []cek.Value {
	out := make([]cek.Value, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case float64:
			out = append(out, cek.Value{Tag: cek.TagF64, N: v})
		case string:
			out = append(out, cek.Value{Tag: cek.TagStr, S: v})
		case bool:
			n := 0.0
			if v {
				n = 1
			}
			out = append(out, cek.Value{Tag: cek.TagBool, N: n})
		default:
			panic("corpus input kind not covered")
		}
	}
	return out
}

func oracleArgs(in []any) []oracle.Value {
	out := make([]oracle.Value, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case float64:
			out = append(out, oracle.Value{Kind: oracle.VNum, Num: v})
		case string:
			out = append(out, oracle.Value{Kind: oracle.VStr, Str: v})
		case bool:
			out = append(out, oracle.Value{Kind: oracle.VBool, Bool: v})
		default:
			panic("corpus input kind not covered")
		}
	}
	return out
}

// --- observable projection ------------------------------------------------------

// obs is the flattened comparison record for one (case, vector) run.
type obs struct {
	verdict string // "value" | "violation:pre" | "violation:post" | "throw" | "error" | "park:<class>"
	effects string // effect classes joined by ","
	value   string // canonical rendering; "" unless verdict == "value"
}

// prodObs projects a production Outcome to the comparison record.
func prodObs(out cek.Outcome) obs {
	o := obs{}
	switch out.Kind {
	case cek.OutDone:
		o.verdict = "value"
		o.value = renderCek(out.Value)
	case cek.OutParked:
		cls := ""
		if out.Condition != nil {
			cls = out.Condition.Class
		}
		switch cls {
		case "contract.pre.violated":
			o.verdict = "violation:pre"
		case "contract.post.violated":
			o.verdict = "violation:post"
		default:
			o.verdict = "park:" + cls
		}
	case cek.OutFaulted:
		o.verdict = "throw"
	default:
		o.verdict = "error"
	}
	classes := make([]string, 0, len(out.Effects))
	for _, e := range out.Effects {
		classes = append(classes, e.Class)
	}
	o.effects = strings.Join(classes, ",")
	return o
}

// refObs projects a reference Result to the comparison record.
func refObs(res oracle.Result) obs {
	o := obs{}
	switch res.Kind {
	case "value":
		o.verdict = "value"
		o.value = oracle.Render(res.Value)
	case "violation":
		o.verdict = "violation:" + res.Clause
	case "throw":
		o.verdict = "throw"
	default:
		o.verdict = "error"
	}
	o.effects = strings.Join(res.Effects, ",")
	return o
}

// renderCek canonically renders a production cek.Value with the SAME projection
// format oracle.Render uses. This is harness-owned comparison code (neither
// evaluator's), so sharing the format string is not sharing an evaluator path.
func renderCek(v cek.Value) string {
	var sb strings.Builder
	renderCekInto(&sb, v)
	return sb.String()
}

func renderCekInto(sb *strings.Builder, v cek.Value) {
	switch v.Tag {
	case cek.TagUndefined:
		sb.WriteString("undefined")
	case cek.TagNull:
		sb.WriteString("null")
	case cek.TagBool:
		sb.WriteString(strconv.FormatBool(v.N != 0))
	case cek.TagF64:
		sb.WriteString("num:" + oracle.NumToStr(v.N))
	case cek.TagStr:
		sb.WriteString(strconv.Quote(v.S))
	case cek.TagArray:
		arr := v.Ref.(*cek.ArrayObj)
		sb.WriteString("[")
		for i, el := range arr.Elems {
			if i > 0 {
				sb.WriteString(",")
			}
			renderCekInto(sb, el)
		}
		sb.WriteString("]")
	case cek.TagRecord:
		rec := v.Ref.(*cek.RecordObj)
		sb.WriteString("{")
		keys := append([]string(nil), rec.Keys...)
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(strconv.Quote(k) + ":")
			renderCekInto(sb, rec.M[k])
		}
		sb.WriteString("}")
	case cek.TagClosure, cek.TagCapToken:
		sb.WriteString("<function>")
	default:
		sb.WriteString("<opaque>")
	}
}

// --- the harness ----------------------------------------------------------------

// runCorpus runs every (case, vector) through both evaluators and returns the
// list of divergences (empty ⇒ the oracle is green).
func runCorpus(t *testing.T, im *admission.Image, cases []loweredCase) []string {
	t.Helper()
	// The std native bodies for the production machine's DefSource.
	stdDefs := map[string]*rast.Node{}
	intrinsics := map[string]string{}
	for _, e := range im.Entries {
		stdDefs[e.Hash] = e.Body
		intrinsics[e.Hash] = e.Intrinsic
	}

	var divergences []string
	for _, lc := range cases {
		src := cek.MapSource{}
		for h, b := range stdDefs {
			src[h] = b
		}
		for h, b := range lc.defs {
			src[h] = b
		}
		interp := cek.New(src, im.Registry())

		ref := &oracle.Machine{Defs: lc.defs, Intrinsics: intrinsics}

		for vi, input := range lc.c.Inputs {
			out := interp.Run(context.Background(), cek.RunReq{
				DefHash:   lc.entryHash,
				Args:      cekArgs(input),
				Tier:      cek.TierTrusted,
				Principal: cek.Principal{Subject: "oracle", IsOperator: true},
			})
			p := prodObs(out)
			r := refObs(ref.Run(lc.entryHash, oracleArgs(input)))

			tag := lc.c.Name + "[" + strconv.Itoa(vi) + "]"
			if p.verdict != r.verdict {
				divergences = append(divergences,
					tag+": verdict machine="+p.verdict+" reference="+r.verdict)
				continue
			}
			if p.effects != r.effects {
				divergences = append(divergences,
					tag+": effect order machine=["+p.effects+"] reference=["+r.effects+"]")
			}
			if p.verdict == "value" && p.value != r.value {
				divergences = append(divergences,
					tag+": produced value machine="+p.value+" reference="+r.value)
			}
		}
	}
	return divergences
}

// TestOracleCorpusGreen: the production machine and the independent reference
// reducer agree on every observable across the whole corpus. Any divergence is
// red (R1-02: a green pipeline is impossible while the oracle is red).
func TestOracleCorpusGreen(t *testing.T) {
	im := admission.BuildImage()
	cases := lowerCorpus(t, im)
	if d := runCorpus(t, im, cases); len(d) > 0 {
		t.Fatalf("oracle divergences (%d):\n  %s", len(d), strings.Join(d, "\n  "))
	}
}

// TestOracleSeededMutantsCaught: the oracle validated against itself (R1-02
// seeded wrong-evaluation requirement). For each registered evaluator mutant —
// one per covered layer — arming it MUST cause at least one caught divergence;
// a surviving mutant (corpus stays green under a deliberately-broken evaluator)
// is a coverage hole in the oracle itself and blocks the release.
func TestOracleSeededMutantsCaught(t *testing.T) {
	im := admission.BuildImage()
	cases := lowerCorpus(t, im)

	// Precondition: the unarmed baseline is green (divergences caused below are
	// then attributable to the armed mutant alone).
	if d := runCorpus(t, im, cases); len(d) > 0 {
		t.Fatalf("baseline not green; mutant attribution impossible:\n  %s", strings.Join(d, "\n  "))
	}

	if len(mutants.Evaluator) != 3 {
		t.Fatalf("want exactly 3 evaluator mutants (one per covered layer), have %d", len(mutants.Evaluator))
	}

	mutants.Arm()
	defer mutants.Disarm()
	for _, mu := range mutants.Evaluator {
		mutants.Enable(mu.Name)
		d := runCorpus(t, im, cases)
		mutants.Disable(mu.Name)
		if len(d) == 0 {
			t.Errorf("SURVIVING EVALUATOR MUTANT %s (%s): corpus stayed green under a "+
				"deliberately-broken evaluator — an oracle coverage hole that blocks the release (%s)",
				mu.Name, mu.Component, mu.Weakens)
			continue
		}
		t.Logf("mutant %s caught: %d divergence(s), first: %s", mu.Name, len(d), d[0])
	}
}
