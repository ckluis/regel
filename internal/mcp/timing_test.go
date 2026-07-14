package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"
)

// mkGetReq builds a real tools/call catalog.get JSON-RPC request for a qname.
func mkGetReq(qname string) rpcRequest {
	args, _ := json.Marshal(map[string]any{"name": "catalog.get", "arguments": map[string]any{"qname": qname}})
	return rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: args}
}

// timing_test.go is the ADR-12 §3 timing red-path: a real out-of-scope name and a
// hallucinated name must be statistically indistinguishable in latency, not merely
// byte-identical. The two-sample test (KS statistic + p99 gap) is proven
// LOAD-BEARING: with the floor bypassed and a fast-path leak seeded it SEPARATES the
// distributions (would fail the release); with the floor restored it cannot.

// ksStat is the two-sample Kolmogorov–Smirnov statistic: the max gap between the two
// empirical CDFs. ~0 ⇒ same distribution; ~1 ⇒ cleanly separable.
func ksStat(a, b []float64) float64 {
	as := append([]float64(nil), a...)
	bs := append([]float64(nil), b...)
	sort.Float64s(as)
	sort.Float64s(bs)
	i, j := 0, 0
	var d float64
	for i < len(as) && j < len(bs) {
		if as[i] <= bs[j] {
			i++
		} else {
			j++
		}
		fa := float64(i) / float64(len(as))
		fb := float64(j) / float64(len(bs))
		if gap := abs(fa - fb); gap > d {
			d = gap
		}
	}
	return d
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func p99(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	idx := int(0.99 * float64(len(s)-1))
	return s[idx]
}

func TestTimingIndistinguishable(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short")
	}
	w := setupMCP(t)
	w.seedOtherOrgFn() // a REAL out-of-scope (org2) name; the org1 agent cannot see it.

	realQ := "app/secret/secretf@org." + otherOrg
	hallQ := "app/nope/ghost@org." + otherOrg

	measure := func(qname string, n int) []float64 {
		out := make([]float64, 0, n)
		for i := 0; i < n; i++ {
			start := time.Now()
			w.srv.Dispatch(context.Background(), &Session{APIKey: agentKey}, mkGetReq(qname))
			out = append(out, float64(time.Since(start).Microseconds()))
		}
		return out
	}

	const N = 150
	measure(realQ, 20) // warm the pool

	// GREEN: the floor makes the two indistinguishable.
	a := measure(realQ, N)
	b := measure(hallQ, N)
	ks := ksStat(a, b)
	gap := abs(p99(a) - p99(b))
	t.Logf("floor ON: ks=%.3f p99gap=%.0fµs", ks, gap)
	if ks > 0.5 {
		t.Fatalf("floor should make out-of-scope and hallucinated indistinguishable (ks=%.3f)", ks)
	}

	// LOAD-BEARING: bypass the floor AND seed a fast-path leak ⇒ the SAME test now
	// separates the distributions (an existence oracle through the clock).
	ResolutionFloor = 0
	leakOutOfScope = true
	a2 := measure(realQ, N)
	b2 := measure(hallQ, N)
	ks2 := ksStat(a2, b2)
	t.Logf("floor OFF + leak: ks=%.3f", ks2)
	// restore before asserting so a failure doesn't poison other tests.
	ResolutionFloor = 4 * time.Millisecond
	leakOutOfScope = false
	if ks2 < 0.6 {
		t.Fatalf("a seeded fast-path leak with the floor bypassed MUST be detectable (ks=%.3f) — the timing test is not load-bearing", ks2)
	}

	// Restore + confirm green again (the floor closes the leak).
	a3 := measure(realQ, N)
	b3 := measure(hallQ, N)
	if ks3 := ksStat(a3, b3); ks3 > 0.5 {
		t.Fatalf("restoring the floor should re-close the timing channel (ks=%.3f)", ks3)
	}
}
