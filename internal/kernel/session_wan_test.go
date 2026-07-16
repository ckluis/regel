package kernel

// session_wan_test.go is the ADR-11 §9 felt-latency machine gate: the reference-app
// clickthrough driven over the named `wan-150` profile with the throttle injected by
// the harness on BOTH directions (deterministic simulated link — no OS shaping), a
// keystroke/blur input path and a single-mutation submit action, measuring p95 over
// enough iterations.
//
// The gate runs RED first (pure server round trip, no echo — the ADR's own forcing
// function) and then GREEN (minimal optimistic local echo landed via §9): input→echo
// ≤ 50 ms and action→confirmed-commit render ≤ 300 ms. Both RED and GREEN numbers are
// captured; the GREEN input→echo/action→commit p95 are the M4 release-gate perf_budget
// rows, the RED numbers are recorded as the witness.
//
// Modeling choice (named in the BUILD-D report): per §3 duty (b) / §9, "the first
// visible UI change" is the slot-map morph — the server frame authoritatively sets
// slot values; the harness has no native DOM, so the morph IS the echo. Under a pure
// server round trip that morph cannot beat one wan-150 RTT (≈150 ms); optimistic local
// echo makes the originating input slot's morph local and instant.
//
// Explicit invocation:
//
//	go test ./internal/kernel/ -run TestWanFeltLatencyGate -count=1 -timeout 300s -v

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/ui"
)

// wanProfile is a deterministic simulated link: a one-way propagation delay (rtt/2
// each direction) plus a serialization delay of size/bandwidth, applied by the
// harness to both directions — no OS-level traffic shaping.
type wanProfile struct {
	rtt     time.Duration
	downBps int // bytes/sec (1.6 Mbps = 200_000 B/s)
	upBps   int // bytes/sec (768 Kbps  =  96_000 B/s)
}

var wan150 = wanProfile{rtt: 150 * time.Millisecond, downBps: 200_000, upBps: 96_000}

func (p wanProfile) up(n int) time.Duration {
	return p.rtt/2 + time.Duration(float64(n)/float64(p.upBps)*float64(time.Second))
}
func (p wanProfile) down(n int) time.Duration {
	return p.rtt/2 + time.Duration(float64(n)/float64(p.downBps)*float64(time.Second))
}

// wanDriver wraps a mounted harness + its SSE connection with the throttle.
type wanDriver struct {
	t *testing.T
	p wanProfile
	h *harness
	c *sseConn
}

// rawPost fires an event POST through the UP-link: sleep the up-link delay, then POST.
// The server pushes the resulting SSE frame during the handler (before the response),
// so the frame path is measured independently via nextFrame's down-link delay.
func (wd *wanDriver) rawPost(event, slotID, value string) {
	body := fmt.Sprintf(`{"slotId":%q,"event":%q,"value":%q,"eventSeq":%d}`, slotID, event, value, wd.h.cursor)
	time.Sleep(wd.p.up(len(body)))
	resp, err := http.Post(wd.h.base+"/session/"+wd.h.sid+"/event", "application/json", strings.NewReader(body))
	if err != nil {
		wd.t.Fatalf("wan POST: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// nextFrame waits for the next server-pushed frame, applies the DOWN-link delay based
// on its wire size, applies it to the harness (maintaining the slot map + digest +
// pending reconciliation), and returns the wall time at which it became "visible".
func (wd *wanDriver) nextFrame(timeout time.Duration) (ui.Frame, time.Time) {
	f := wd.c.nextFrame(wd.t, timeout)
	time.Sleep(wd.p.down(len(ui.EncodeFrame(f))))
	wd.h.applyFrame(f)
	return f, time.Now()
}

// drain consumes any straggler frames (the submit's self-invalidation re-render) until
// the SSE channel is quiet for a settle window, so the next iteration starts clean.
func (wd *wanDriver) drain(settle time.Duration) {
	for {
		select {
		case f := <-wd.c.frames:
			wd.h.applyFrame(f)
		case <-time.After(settle):
			return
		}
	}
}

func TestWanFeltLatencyGate(t *testing.T) {
	if testing.Short() {
		t.Skip("wan-150 felt-latency gate is a heavy M4 gate")
	}
	const iters = 30

	// RED: pure server round trip (no echo) — the forcing function.
	redInput, redCommit := runWanGate(t, false, iters)
	// GREEN: minimal optimistic local echo on input-class events.
	greenInput, greenCommit := runWanGate(t, true, iters)

	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	t.Logf("WAN-150 GATE (iters=%d, RTT=150ms 1.6Mbps/768Kbps):\n"+
		"  RED  (no echo): input→echo p95=%.1fms  action→commit p95=%.1fms\n"+
		"  GREEN (echo)  : input→echo p95=%.1fms  action→commit p95=%.1fms",
		iters, ms(redInput), ms(redCommit), ms(greenInput), ms(greenCommit))

	// The forcing function: a pure server round trip CANNOT meet input→echo ≤ 50 ms.
	if ms(redInput) <= 50 {
		t.Fatalf("RED input→echo p95=%.1fms ≤ 50ms — the §9 forcing function did not fire", ms(redInput))
	}
	// Both RED and GREEN action→commit fit the 300 ms budget (echo cannot shortcut the
	// real commit round trip; the budget proves the round trip itself is fast enough).
	if ms(redCommit) > 300 {
		t.Fatalf("RED action→commit p95=%.1fms exceeds 300ms (the commit round trip itself is too slow)", ms(redCommit))
	}
	// GREEN: echo turns the input→echo gate green; action→commit stays green.
	if ms(greenInput) > 50 {
		t.Fatalf("GREEN input→echo p95=%.1fms exceeds 50ms — echo did not turn the gate green", ms(greenInput))
	}
	if ms(greenCommit) > 300 {
		t.Fatalf("GREEN action→commit p95=%.1fms exceeds 300ms", ms(greenCommit))
	}

	// perf_budget rows: the GREEN numbers are the M4 release gate; the RED numbers are
	// the recorded witness (record-only, no assertion — they are expected to breach).
	se := newSessionEnv(t)
	writeStormBudget(t, se, "sse.wan150.input_echo_ms_p95", 50, ms(greenInput))
	writeStormBudget(t, se, "sse.wan150.action_commit_ms_p95", 300, ms(greenCommit))
	recordBudget(t, se, "sse.wan150.input_echo_ms_p95.noecho", 50, ms(redInput))
	recordBudget(t, se, "sse.wan150.action_commit_ms_p95.noecho", 300, ms(redCommit))
}

// runWanGate drives `iters` reference clickthroughs over wan-150 and returns the p95
// input→echo and action→commit latencies. Each clickthrough: type a fresh name into
// the form's name field (input→echo), then submit (action→confirmed-commit render).
func runWanGate(t *testing.T, echo bool, iters int) (inputP95, commitP95 time.Duration) {
	t.Helper()
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "start", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	h := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	h.echoOn = echo
	c := h.openSSE(0)
	defer c.close()
	wd := &wanDriver{t: t, p: wan150, h: h, c: c}
	time.Sleep(200 * time.Millisecond) // let the SSE subscription register

	inputs := make([]time.Duration, 0, iters)
	commits := make([]time.Duration, 0, iters)
	for i := 0; i < iters; i++ {
		value := fmt.Sprintf("Name%03d", i)

		// --- input → echo ---
		t0 := time.Now()
		if echo {
			// Optimistic local echo: the originating slot morphs instantly (§9).
			h.localEcho(nameForm, value)
			inputs = append(inputs, time.Since(t0))
			// Fire the input to the server for reconciliation + consume its frame.
			wd.rawPost("input", nameForm, value)
			wd.nextFrame(4 * time.Second)
		} else {
			// Pure server round trip: first visible change is the server frame.
			wd.rawPost("input", nameForm, value)
			_, visibleAt := wd.nextFrame(4 * time.Second)
			inputs = append(inputs, visibleAt.Sub(t0))
		}

		// --- action → confirmed-commit render ---
		t1 := time.Now()
		wd.rawPost("submit", "", "")
		_, committedAt := wd.nextFrame(4 * time.Second)
		commits = append(commits, committedAt.Sub(t1))

		// Drain the submit's self-invalidation re-render frame(s) before the next round.
		wd.drain(400 * time.Millisecond)
	}

	sort.Slice(inputs, func(i, j int) bool { return inputs[i] < inputs[j] })
	sort.Slice(commits, func(i, j int) bool { return commits[i] < commits[j] })
	return inputs[minInt(len(inputs)*95/100, len(inputs)-1)], commits[minInt(len(commits)*95/100, len(commits)-1)]
}

// recordBudget writes a perf_budget row WITHOUT the pass/fail assertion — used for the
// RED wan-150 witness numbers, which are expected to breach the release budget.
func recordBudget(t *testing.T, se *sessionEnv, metric string, budget, measured float64) {
	t.Helper()
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(context.Background(), `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, $1, 'trusted', $2, $3, 'M4')
ON CONFLICT (epoch, metric) DO UPDATE SET measured=EXCLUDED.measured, budget=EXCLUDED.budget`,
			metric, budget, measured); err != nil {
			t.Fatalf("record perf_budget %s: %v", metric, err)
		}
	})
}
