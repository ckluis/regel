package m5eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// harness.go is the resumable driver for the three M5 gates. The DB layer (pins,
// per-attempt persistence, metric computation, the fuel derivation, and the
// mechanized flip write) is deterministic and unit-testable without an LLM; the
// LLM door (claude -p) and the MCP subprocess client sit at the bottom. Nothing
// here writes a metric it did not compute from persisted real-run rows.

// ---------------------------------------------------------------------------
// DB layer
// ---------------------------------------------------------------------------

func CurrentEpoch(ctx context.Context, conn *pgwire.Conn) (int, error) {
	var n int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// EnsurePin inserts the (epoch, kind) pin if absent, or verifies an existing pin
// still matches the on-disk corpus hash + k. A mismatch is a TAMPERED/STALE pin
// (k changed, or corpus edited): the harness refuses to score against it. This is
// the REVIEW-PRE-E §4 L2 fix — k is frozen with the corpus, provable after the
// fact.
func EnsurePin(ctx context.Context, conn *pgwire.Conn, epoch int, kind string, k int, corpusHash string, size int) error {
	var (
		haveK    int
		haveHash string
	)
	found, err := conn.QueryRow(ctx,
		`SELECT k, corpus_hash FROM eval_pin WHERE epoch=$1 AND corpus_kind=$2`,
		[]any{epoch, kind}, &haveK, &haveHash)
	if err != nil {
		return err
	}
	if !found {
		_, err = conn.Exec(ctx, `
INSERT INTO eval_pin (epoch, corpus_kind, k, corpus_hash, corpus_size)
VALUES ($1,$2,$3,$4,$5)`, epoch, kind, k, corpusHash, size)
		return err
	}
	if haveK != k {
		return fmt.Errorf("TAMPERED PIN (%s epoch %d): pinned k=%d but corpus/harness k=%d — a new pin + re-run is required", kind, epoch, haveK, k)
	}
	if haveHash != corpusHash {
		return fmt.Errorf("TAMPERED PIN (%s epoch %d): pinned corpus_hash %s no longer matches on-disk corpus %s — a new pin + re-run is required", kind, epoch, short(haveHash), short(corpusHash))
	}
	return nil
}

func short(h string) string {
	if i := strings.IndexByte(h, ':'); i >= 0 && len(h) > i+13 {
		return h[:i+13] + "…"
	}
	return h
}

// AttemptResult is one persisted (task, attempt) row.
type AttemptResult struct {
	TaskID     string
	Attempt    int
	Admitted   bool
	BehaviorOK bool
	Passed     bool
	Iterations int
	FuelUsed   float64
	Detail     map[string]any
}

func SaveResult(ctx context.Context, conn *pgwire.Conn, epoch int, kind string, r AttemptResult) error {
	det, _ := json.Marshal(r.Detail)
	_, err := conn.Exec(ctx, `
INSERT INTO m5_eval_result (epoch, corpus_kind, task_id, attempt, admitted, behavior_ok, passed, iterations, fuel_used, detail)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
ON CONFLICT (epoch, corpus_kind, task_id, attempt) DO UPDATE SET
  admitted=EXCLUDED.admitted, behavior_ok=EXCLUDED.behavior_ok, passed=EXCLUDED.passed,
  iterations=EXCLUDED.iterations, fuel_used=EXCLUDED.fuel_used, detail=EXCLUDED.detail, created_at=now()`,
		epoch, kind, r.TaskID, r.Attempt, r.Admitted, r.BehaviorOK, r.Passed, r.Iterations, r.FuelUsed, string(det))
	return err
}

// DoneAttempts returns task_id → set of completed attempt numbers (for resume).
func DoneAttempts(ctx context.Context, conn *pgwire.Conn, epoch int, kind string) (map[string]map[int]bool, error) {
	rows, err := conn.Query(ctx,
		`SELECT task_id, attempt FROM m5_eval_result WHERE epoch=$1 AND corpus_kind=$2`, epoch, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]bool{}
	for rows.Next() {
		var id string
		var att int
		if err := rows.Scan(&id, &att); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[int]bool{}
		}
		out[id][att] = true
	}
	return out, nil
}

// LoadResults returns all persisted results for (epoch, kind).
func LoadResults(ctx context.Context, conn *pgwire.Conn, epoch int, kind string) ([]AttemptResult, error) {
	rows, err := conn.Query(ctx, `
SELECT task_id, attempt, admitted, behavior_ok, passed, iterations, fuel_used
  FROM m5_eval_result WHERE epoch=$1 AND corpus_kind=$2
 ORDER BY task_id, attempt`, epoch, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttemptResult
	for rows.Next() {
		var r AttemptResult
		if err := rows.Scan(&r.TaskID, &r.Attempt, &r.Admitted, &r.BehaviorOK, &r.Passed, &r.Iterations, &r.FuelUsed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// WriteGate upserts a computed m5_gate row.
func WriteGate(ctx context.Context, conn *pgwire.Conn, epoch int, gate string,
	corpusSize, floorSize int, measured, floor float64, green, partial bool, detail map[string]any) error {
	det, _ := json.Marshal(detail)
	_, err := conn.Exec(ctx, `
INSERT INTO m5_gate (epoch, gate, corpus_size, floor_size, measured, floor, green, partial, detail)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
ON CONFLICT (epoch, gate) DO UPDATE SET
  corpus_size=EXCLUDED.corpus_size, floor_size=EXCLUDED.floor_size, measured=EXCLUDED.measured,
  floor=EXCLUDED.floor, green=EXCLUDED.green, partial=EXCLUDED.partial, detail=EXCLUDED.detail,
  computed_at=now()`,
		epoch, gate, corpusSize, floorSize, measured, floor, green, partial, string(det))
	return err
}

// ---------------------------------------------------------------------------
// Metric computation (from persisted results — never invented)
// ---------------------------------------------------------------------------

// AuthoringMetrics is the computed §3a result.
type AuthoringMetrics struct {
	N           int     // tasks with at least one completed attempt
	K           int     // pinned k
	PassAt1     float64 // fraction green on attempt 1
	PassAtK     float64 // fraction green within k attempts
	P95Iter     float64 // iterations-to-green P95 over passing attempts
	Green       bool    // pass@1 >= 0.5 AND pass@k >= 0.9
	Partial     bool    // fewer than the pinned corpus was completed (LLM died / sample)
	CompletedN  int
	CorpusN     int
	PerTask     map[string]bool // task_id → passed within k
	IterSamples []int
}

func ComputeAuthoring(results []AttemptResult, k, corpusN int) AuthoringMetrics {
	byTask := map[string][]AttemptResult{}
	for _, r := range results {
		byTask[r.TaskID] = append(byTask[r.TaskID], r)
	}
	m := AuthoringMetrics{K: k, CorpusN: corpusN, PerTask: map[string]bool{}}
	var pass1, passK, tasks1 int
	for id, rs := range byTask {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Attempt < rs[j].Attempt })
		m.N++
		// attempt 1
		for _, r := range rs {
			if r.Attempt == 1 {
				tasks1++
				if r.Passed {
					pass1++
				}
				break
			}
		}
		// pass within k
		green := false
		for _, r := range rs {
			if r.Attempt <= k && r.Passed {
				green = true
				m.IterSamples = append(m.IterSamples, r.Iterations)
				break
			}
		}
		if green {
			passK++
		}
		m.PerTask[id] = green
	}
	m.CompletedN = m.N
	if tasks1 > 0 {
		m.PassAt1 = float64(pass1) / float64(tasks1)
	}
	if m.N > 0 {
		m.PassAtK = float64(passK) / float64(m.N)
	}
	m.P95Iter = percentileInt(m.IterSamples, 0.95)
	m.Green = m.PassAt1 >= FloorPassAt1 && m.PassAtK >= FloorPassAtK
	m.Partial = m.N < corpusN
	return m
}

// RestartMetrics is the computed §7 result.
type RestartMetrics struct {
	M        int
	Correct  int
	Accuracy float64
	Green    bool
	Partial  bool
	CorpusM  int
	PerCase  map[string]bool
}

func ComputeRestart(results []AttemptResult, corpusM int) RestartMetrics {
	m := RestartMetrics{CorpusM: corpusM, PerCase: map[string]bool{}}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.TaskID] {
			continue
		}
		seen[r.TaskID] = true
		m.M++
		if r.Passed {
			m.Correct++
		}
		m.PerCase[r.TaskID] = r.Passed
	}
	if m.M > 0 {
		m.Accuracy = float64(m.Correct) / float64(m.M)
	}
	// Green requires BOTH the accuracy floor AND the ADR §7 corpus floor (M>=30)
	// AND a complete run (not partial).
	m.Partial = m.M < corpusM
	m.Green = m.Accuracy >= FloorRestartAcc && m.M >= FloorRestartM && !m.Partial
	return m
}

// DeriveFuelCapacity applies the ADR-12 §5 formula from the measured authoring
// P95 iterations-to-green and returns the capacity, writing the governing
// admission_capacity row (derived_from traced to the pin) plus the §5 m5_gate
// row. Assert: the capacity must COVER the corpus (max fuel a passing attempt
// used <= capacity), else the §5 red-path fires.
func DeriveFuelCapacity(ctx context.Context, conn *pgwire.Conn, epoch int, am AuthoringMetrics, results []AttemptResult) (float64, bool, error) {
	capacity := math.Ceil(am.P95Iter * CostFullPipeline * FuelMargin)
	if capacity < 1 {
		capacity = 1
	}
	// Max fuel a passing attempt actually burned — the coverage check.
	maxPassFuel := 0.0
	for _, r := range results {
		if r.Passed && r.FuelUsed > maxPassFuel {
			maxPassFuel = r.FuelUsed
		}
	}
	covers := capacity >= maxPassFuel
	derivedFrom := fmt.Sprintf("eval:epoch=%d:p95_iter=%.2f:formula=ceil(p95*%.0f*%.1f)", epoch, am.P95Iter, CostFullPipeline, FuelMargin)
	if _, err := conn.Exec(ctx, `
UPDATE admission_capacity SET capacity=$1, derived_from=$2 WHERE agent_kind='agent'`,
		capacity, derivedFrom); err != nil {
		return 0, false, err
	}
	detail := map[string]any{
		"p95_iterations_to_green": am.P95Iter,
		"cost_full_pipeline":      CostFullPipeline,
		"margin":                  FuelMargin,
		"derived_capacity":        capacity,
		"max_passing_fuel_used":   maxPassFuel,
		"covers_corpus":           covers,
		"derived_from":            derivedFrom,
	}
	// measured=capacity, floor=maxPassFuel; green iff it covers the corpus.
	if err := WriteGate(ctx, conn, epoch, "fuel", am.N, 0, capacity, maxPassFuel, covers, am.Partial, detail); err != nil {
		return 0, false, err
	}
	return capacity, covers, nil
}

func percentileInt(xs []int, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	idx := int(math.Ceil(p*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return float64(s[idx])
}

// stageCostLocal mirrors internal/admission.stageCost (harness-side, not a kernel
// dependency) so the harness can price each submission by its deepest stage.
func stageCostLocal(stage string) float64 {
	switch stage {
	case "", "typecheck-budget", "lower", "seeders":
		return 1
	case "insert":
		return 2
	case "tsgo":
		return 3
	default:
		return 5
	}
}

// ---------------------------------------------------------------------------
// MCP subprocess client (JSON-RPC 2.0 over stdio)
// ---------------------------------------------------------------------------

// MCPSession drives a live `regel mcp --key KEY` subprocess over stdio. The
// server emits exactly one response line per request, in order.
type MCPSession struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

// StartMCP spawns the real agent-plane subprocess bound to key, with dsn in env.
func StartMCP(ctx context.Context, binPath, key, dsn string) (*MCPSession, error) {
	cmd := exec.CommandContext(ctx, binPath, "mcp", "--key", key)
	cmd.Env = append([]string{"REGEL_PG_DSN=" + dsn}, envNoDSN()...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(outPipe)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	s := &MCPSession{cmd: cmd, in: in, out: sc}
	if _, err := s.Call("initialize", nil); err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	return s, nil
}

// Call sends one JSON-RPC request and returns the parsed response object.
func (s *MCPSession) Call(method string, params any) (map[string]any, error) {
	s.id++
	req := map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	if !s.out.Scan() {
		if err := s.out.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("mcp: no response (server closed)")
	}
	var resp map[string]any
	if err := json.Unmarshal(s.out.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("mcp: bad response %q: %w", s.out.Text(), err)
	}
	return resp, nil
}

// Tool calls a tool and returns the decoded inner tool payload (result.content[0].text
// parsed as JSON) — the shape the tools return.
func (s *MCPSession) Tool(name string, args map[string]any) (map[string]any, error) {
	resp, err := s.Call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	res, _ := resp["result"].(map[string]any)
	if res == nil {
		return resp, nil // error responses carry no result; return as-is
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return res, nil
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var inner map[string]any
	if json.Unmarshal([]byte(text), &inner) == nil {
		return inner, nil
	}
	return map[string]any{"text": text}, nil
}

// envNoDSN returns the process environment with any REGEL_PG_DSN removed, so the
// subprocess picks up exactly the DSN the harness sets.
func envNoDSN() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "REGEL_PG_DSN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (s *MCPSession) Close() {
	if s.in != nil {
		s.in.Close()
	}
	if s.cmd != nil {
		_ = s.cmd.Wait()
	}
}

// ---------------------------------------------------------------------------
// LLM door (claude -p), with a per-call timeout
// ---------------------------------------------------------------------------

// LLMCall runs `claude -p PROMPT` with a hard timeout and returns stdout. It is
// the ONLY door to the model — serial, one call at a time, no parallelism.
func LLMCall(prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("LLM timeout after %s", timeout)
		}
		return string(out), fmt.Errorf("claude -p: %w", err)
	}
	return string(out), nil
}

// extractTS pulls TypeScript source out of an LLM completion: it strips a fenced
// code block if present, else returns the trimmed text.
func extractTS(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		// drop an optional language tag on the fence line
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return s
}
