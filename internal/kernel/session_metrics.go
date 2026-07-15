package kernel

// session_metrics.go publishes the ADR-13 §2 reactive-layer golden signals on the
// same package-atomic pattern as cfr.Metrics: sse.resyncs_total (a client-bug
// alarm), sse.invalidation_depth (queue depth gauge), sse.fanout_lag_ms
// (enqueue→patch-sent drain lag gauge). Surfaced through /healthz.

import "sync/atomic"

var (
	mResyncsTotal      int64
	mInvalidationDepth int64 // gauge: current queue depth
	mFanoutLagMS       int64 // gauge: last observed enqueue→frame drain lag
	mFramesSent        int64 // denominator for the resync-rate SLO
)

// SSEMetrics is a snapshot of the reactive-layer signals (ADR-13 §2).
type SSEMetrics struct {
	ResyncsTotal      int64 `json:"sse.resyncs_total"`
	InvalidationDepth int64 `json:"sse.invalidation_depth"`
	FanoutLagMS       int64 `json:"sse.fanout_lag_ms"`
	FramesSent        int64 `json:"sse.frames_sent"`
}

func sseMetricsSnapshot() SSEMetrics {
	return SSEMetrics{
		ResyncsTotal:      atomic.LoadInt64(&mResyncsTotal),
		InvalidationDepth: atomic.LoadInt64(&mInvalidationDepth),
		FanoutLagMS:       atomic.LoadInt64(&mFanoutLagMS),
		FramesSent:        atomic.LoadInt64(&mFramesSent),
	}
}

func incResyncs()           { atomic.AddInt64(&mResyncsTotal, 1) }
func incFramesSent()        { atomic.AddInt64(&mFramesSent, 1) }
func addInvalDepth(n int64) { atomic.AddInt64(&mInvalidationDepth, n) }
func setFanoutLag(ms int64) { atomic.StoreInt64(&mFanoutLagMS, ms) }
