package cek

import (
	"context"
	"testing"

	"regel.dev/regel/internal/lower"
)

// benchProgram is the M0 reference microbench: a tight arithmetic + self-call
// loop (ADR-04 §8). It exercises the trusted governor loop's hot path.
const benchProgram = `export function bench(n: number): number {
  let acc = 0;
  let i = 0;
  while (i < n) {
    acc = acc + i * 2 - 1;
    i = i + 1;
  }
  return acc;
}`

func benchInterp(b *testing.B) (*Interp, string) {
	b.Helper()
	r := lower.Module(benchProgram, lower.ModuleContext{ModuleName: "app/bench"})
	if !r.OK() {
		b.Fatalf("lower: %v", r.Diagnostics)
	}
	src := MapSource{}
	var hash string
	for _, d := range r.Definitions {
		src[d.Hash] = d.Body
		if d.Name == "bench" {
			hash = d.Hash
		}
	}
	return New(src, nil), hash
}

// BenchmarkCEKStepsPerSec measures trusted-governor transitions/sec (ADR-04 §8
// floor: ≥ 1,000,000 transitions/sec/core).
func BenchmarkCEKStepsPerSec(b *testing.B) {
	in, hash := benchInterp(b)
	ctx := context.Background()
	var totalTransitions int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o := in.Run(ctx, RunReq{DefHash: hash, Args: []Value{f64(2000)}, Tier: TierTrusted})
		if o.Kind != OutDone {
			b.Fatalf("kind=%d", o.Kind)
		}
		totalTransitions += o.Transitions
	}
	b.StopTimer()
	secs := b.Elapsed().Seconds()
	if secs > 0 {
		b.ReportMetric(float64(totalTransitions)/secs, "transitions/sec")
	}
	b.ReportMetric(float64(totalTransitions)/float64(b.N), "transitions/op")
}

// BenchmarkMeteringTaxGovernor / *Fuel measure the fuelMeter overhead vs the
// governorMeter on the same program (ADR-04 §8: sandbox tax ≤ 10 %).
func BenchmarkMeteringTaxGovernor(b *testing.B) {
	in, hash := benchInterp(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.Run(ctx, RunReq{DefHash: hash, Args: []Value{f64(2000)}, Tier: TierTrusted})
	}
}

func BenchmarkMeteringTaxFuel(b *testing.B) {
	in, hash := benchInterp(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.Run(ctx, RunReq{DefHash: hash, Args: []Value{f64(2000)}, Tier: TierSandbox, Fuel: 1 << 40, Alloc: 1 << 40})
	}
}
