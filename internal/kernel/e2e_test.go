package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// --- shared binary (built once for the whole kernel test binary) -------------

var (
	e2eBinOnce sync.Once
	e2eBinPath string
	e2eBinDir  string
	e2eBinErr  error
)

// regelBin builds ./cmd/regel exactly once into a temp dir and returns the path.
// TestMain removes the dir when the package's tests finish. Lazy: a `-short` run
// that skips every process test never pays the build cost.
func regelBin(t *testing.T) string {
	t.Helper()
	e2eBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "regel-e2e-")
		if err != nil {
			e2eBinErr = err
			return
		}
		e2eBinDir = dir
		bin := filepath.Join(dir, "regel")
		root, err := filepath.Abs("../..")
		if err != nil {
			e2eBinErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/regel")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			e2eBinErr = fmt.Errorf("go build ./cmd/regel: %v\n%s", err, out)
			return
		}
		e2eBinPath = bin
	})
	if e2eBinErr != nil {
		t.Fatalf("build regel binary: %v", e2eBinErr)
	}
	return e2eBinPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if e2eBinDir != "" {
		_ = os.RemoveAll(e2eBinDir)
	}
	os.Exit(code)
}

// --- process env: fresh DB + migrate + genesis VIA THE BINARY ----------------

type procEnv struct {
	base pgwire.Config
	db   string
	dsn  string
	pool *pgwire.Pool // direct SQL for assertions (same DB the serve process drives)
}

// newProcEnv creates a fresh random database, then runs `regel migrate-db` and
// `regel genesis` against it through the built binary — the real operator path.
func newProcEnv(t *testing.T) *procEnv {
	t.Helper()
	bin := regelBin(t)
	ctx := context.Background()
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	db := randName("regel_e2e_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	dsn := fmt.Sprintf("postgres://%s@%s:%s/%s", base.User, base.Host, base.Port, db)
	e := &procEnv{base: base, db: db, dsn: dsn}
	t.Cleanup(func() {
		if e.pool != nil {
			e.pool.Close()
		}
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})

	if out, err := e.runBin(t, bin, "migrate-db"); err != nil {
		t.Fatalf("migrate-db: %v\n%s", err, out)
	}
	if out, err := e.runBin(t, bin, "genesis"); err != nil {
		t.Fatalf("genesis: %v\n%s", err, out)
	}

	cfg := base
	cfg.Database = db
	e.pool = pgwire.NewPool(cfg, 8)
	return e
}

func (e *procEnv) runBin(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "REGEL_PG_DSN="+e.dsn)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// admit admits source directly against the pool (content-addressed: the hash is
// identical whatever DB it lands in) and returns the resolved head hash.
func (e *procEnv) admit(t *testing.T, src, prefix, name string) string {
	t.Helper()
	v := admitSrc(t, e.pool, src, prefix, nil)
	if v.Outcome != admission.OutcomeAdmitted && v.Outcome != admission.OutcomeAlreadyAdmitted {
		t.Fatalf("admit %s: %q (%+v)", name, v.Outcome, v.Diagnostics)
	}
	h := v.Hashes[prefix+"/"+name]
	if h == "" {
		t.Fatalf("admit %s: no hash for %s/%s (%+v)", name, prefix, name, v.Hashes)
	}
	return h
}

func (e *procEnv) scalar(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	var n int64
	if _, err := conn.QueryRow(context.Background(), sql, args, &n); err != nil {
		t.Fatalf("scalar %q: %v", sql, err)
	}
	return n
}

// exec runs a mutation against the env's DB.
func (e *procEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *procEnv) text(t *testing.T, sql string, args ...any) string {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	var s string
	found, err := conn.QueryRow(context.Background(), sql, args, &s)
	if err != nil {
		t.Fatalf("text %q: %v", sql, err)
	}
	if !found {
		return ""
	}
	return s
}

// outboxTrace returns the ordered (class,step_seq,ordinal) effect trace as a
// stable string — the exactly-once fingerprint the reference and kill legs share.
func (e *procEnv) outboxTrace(t *testing.T, contID string) string {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	rows, err := conn.Query(context.Background(),
		`SELECT class, step_seq, ordinal FROM outbox WHERE continuation_id=$1 ORDER BY step_seq, ordinal`, contID)
	if err != nil {
		t.Fatalf("outbox trace: %v", err)
	}
	var trace string
	for rows.Next() {
		var class string
		var seq, ord int64
		if err := rows.Scan(&class, &seq, &ord); err != nil {
			rows.Close()
			t.Fatalf("scan trace: %v", err)
		}
		trace += fmt.Sprintf("%s@%d.%d;", class, seq, ord)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("trace rows: %v", err)
	}
	return trace
}

// --- serve subprocess --------------------------------------------------------

type serveProc struct {
	cmd     *exec.Cmd
	addr    string
	baseURL string
	logPath string
	stopped bool
}

// spawnServe starts `regel serve` as its own process group against this env's DB
// with the given lease/poll, waits for HTTP readiness, and registers a cleanup
// that SIGKILLs + reaps it. Its own process group means a stray child dies too.
func (e *procEnv) spawnServe(t *testing.T, lease int, poll time.Duration) *serveProc {
	t.Helper()
	bin := regelBin(t)
	addr := freeAddr(t)
	logf, err := os.CreateTemp("", "regel-serve-*.log")
	if err != nil {
		t.Fatalf("serve log: %v", err)
	}
	logPath := logf.Name()
	logf.Close()
	t.Cleanup(func() { os.Remove(logPath) })
	lf, _ := os.OpenFile(logPath, os.O_WRONLY, 0)

	cmd := exec.Command(bin, "serve", "-addr", addr, "-lease", strconv.Itoa(lease), "-poll", poll.String())
	cmd.Env = append(os.Environ(), "REGEL_PG_DSN="+e.dsn)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	if lf != nil {
		lf.Close()
	}
	sp := &serveProc{cmd: cmd, addr: addr, baseURL: "http://" + addr, logPath: logPath}
	t.Cleanup(func() { sp.stop() })
	sp.waitReady(t)
	return sp
}

func (sp *serveProc) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(sp.baseURL + "/healthz")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if sp.cmd.ProcessState != nil { // exited early
			b, _ := os.ReadFile(sp.logPath)
			t.Fatalf("serve exited before ready:\n%s", b)
		}
		time.Sleep(30 * time.Millisecond)
	}
	b, _ := os.ReadFile(sp.logPath)
	t.Fatalf("serve %s not ready in time:\n%s", sp.addr, b)
}

// sigkill delivers a real SIGKILL (kill -9) to the serve process and reaps it —
// no graceful shutdown, the reaper-recovers-a-dead-kernel path under test.
func (sp *serveProc) sigkill(t *testing.T) int {
	t.Helper()
	pid := sp.cmd.Process.Pid
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL %d: %v", pid, err)
	}
	sp.stopped = true
	_ = sp.cmd.Wait()
	return pid
}

func (sp *serveProc) stop() {
	if sp.cmd == nil || sp.cmd.Process == nil || sp.stopped {
		return
	}
	sp.stopped = true
	_ = sp.cmd.Process.Kill()
	_ = sp.cmd.Wait()
}

// --- HTTP client helpers -----------------------------------------------------

func httpPost(t *testing.T, url, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", url, stringsReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// startWorkflow posts POST /workflow/{name} and returns the continuation id.
func (sp *serveProc) startWorkflow(t *testing.T, name string) string {
	t.Helper()
	code, body := httpPost(t, sp.baseURL+"/workflow/"+name, "[]",
		map[string]string{"X-Regel-Actor": "operator:op"})
	if code != 202 {
		t.Fatalf("start workflow %s: %d %q", name, code, body)
	}
	var r struct {
		ContinuationID string `json:"continuation_id"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.ContinuationID == "" {
		t.Fatalf("start workflow body: %q (err=%v)", body, err)
	}
	return r.ContinuationID
}

func (sp *serveProc) healthz(t *testing.T) map[string]any {
	t.Helper()
	resp, err := http.Get(sp.baseURL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("healthz json: %q", b)
	}
	return m
}

func healthzReoffers(t *testing.T, m map[string]any) int64 {
	t.Helper()
	mm, ok := m["metrics"].(map[string]any)
	if !ok {
		return 0
	}
	f, _ := mm["reoffers"].(float64)
	return int64(f)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

// --- ADR-05 TEST 1: kill -9 mid-step, cross-kernel, effect exactly once -------

// killWorkflow: N=4 iterations, each a nontrivial compute loop + a recorded
// channel.send effect + wf.sleep(100). The result aggregates across steps so a
// wrong resume yields a wrong value; the four sends land four outbox rows keyed
// UNIQUE(continuation_id, step_seq, ordinal) — a double effect is a DB violation.
const killWorkflow = `import { sleep, send } from "std/wf";
export function w(): number {
  let acc = 0;
  for (let i = 0; i < 4; i++) {
    let c = 0;
    for (let j = 0; j < 300000; j++) { c = c + 1; }
    acc = acc + (i + 1) * 1000 + (c - 300000);
    send("kstep", acc);
    sleep(100);
  }
  return acc;
}`

func TestKill9CrossKernelExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("kill-9 process test is heavy (builds + spawns servers)")
	}
	const wantResult = int64(10000) // 1000+2000+3000+4000
	const wantOutbox = int64(4)

	// --- REFERENCE LEG: uninterrupted run on a fresh DB ---------------------
	refEnv := newProcEnv(t)
	refHash := refEnv.admit(t, killWorkflow, "app/kill", "w")
	refSrv := refEnv.spawnServe(t, 2, 100*time.Millisecond)
	refID := refSrv.startWorkflow(t, "app/kill/w")
	waitContinuationStatus(t, refEnv, refID, "done", 30*time.Second)
	refVal := loadContinuationResult(t, refEnv, refID)
	refTrace := refEnv.outboxTrace(t, refID)
	refN := refEnv.scalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, refID)
	t.Logf("REFERENCE LEG: id=%s result=%d outbox=%d trace=%q", refID, refVal, refN, refTrace)
	if refVal != wantResult {
		t.Fatalf("reference result = %d, want %d", refVal, wantResult)
	}
	if refN != wantOutbox {
		t.Fatalf("reference outbox = %d, want %d", refN, wantOutbox)
	}
	_ = refHash

	// --- KILL LEG: SIGKILL kernel A mid-step, resume on kernel B ------------
	killEnv := newProcEnv(t)
	killEnv.admit(t, killWorkflow, "app/kill", "w")
	srvA := killEnv.spawnServe(t, 2, 100*time.Millisecond)
	killID := srvA.startWorkflow(t, "app/kill/w")

	// Poll until at least one step has committed (>=1 outbox row) AND a step is
	// genuinely in flight (a task committed 'running'): killing then strands a
	// running task whose lease must expire and be re-offered by kernel B.
	deadline := time.Now().Add(30 * time.Second)
	var killMoment string
	for time.Now().Before(deadline) {
		outN := killEnv.scalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, killID)
		running := killEnv.scalar(t, `SELECT count(*) FROM task WHERE status='running'`)
		st := killEnv.text(t, `SELECT status FROM continuation WHERE id=$1`, killID)
		if outN >= 1 && running >= 1 {
			killMoment = fmt.Sprintf("outbox=%d running_tasks=%d continuation_status=%s step_seq=%d",
				outN, running, st, killEnv.scalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, killID))
			break
		}
		if st == "done" {
			t.Fatal("workflow completed before a kill window opened; increase the compute loop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if killMoment == "" {
		t.Fatal("no kill window (running task + committed step) observed")
	}
	pid := srvA.sigkill(t)
	t.Logf("KILL: SIGKILL kernel A pid=%d at moment [%s]", pid, killMoment)

	// The row is stranded — never 'done', with a running task lease that will
	// expire. (The continuation itself never commits 'running': the whole step
	// is one txn, so a kill rolls it back to its last checkpoint.)
	strandedStatus := killEnv.text(t, `SELECT status FROM continuation WHERE id=$1`, killID)
	strandedRunningTasks := killEnv.scalar(t, `SELECT count(*) FROM task WHERE status='running'`)
	t.Logf("STRANDED: continuation_status=%s running_tasks=%d (lease will expire → reaper re-offers)",
		strandedStatus, strandedRunningTasks)
	if strandedStatus == "done" {
		t.Fatalf("continuation reached done before kill took effect")
	}

	// --- Kernel B: a brand-new process on the SAME DB resumes to completion --
	srvB := killEnv.spawnServe(t, 2, 100*time.Millisecond)
	waitContinuationStatus(t, killEnv, killID, "done", 30*time.Second)
	killVal := loadContinuationResult(t, killEnv, killID)
	killTrace := killEnv.outboxTrace(t, killID)
	killN := killEnv.scalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, killID)
	reoffers := healthzReoffers(t, srvB.healthz(t))
	t.Logf("RESUME (kernel B): id=%s result=%d outbox=%d trace=%q healthz.reoffers=%d",
		killID, killVal, killN, killTrace, reoffers)

	// --- Exactly-once assertions --------------------------------------------
	if killVal != refVal {
		t.Fatalf("kill-leg result = %d, want IDENTICAL to reference %d", killVal, refVal)
	}
	if killN != wantOutbox {
		t.Fatalf("kill-leg outbox = %d, want exactly %d (no double, no missing effect)", killN, wantOutbox)
	}
	if killTrace != refTrace {
		t.Fatalf("kill-leg trace %q != reference trace %q", killTrace, refTrace)
	}
	if reoffers < 1 {
		t.Fatalf("kernel B healthz.reoffers = %d, want >0 (the stranded running task was re-offered)", reoffers)
	}
	t.Logf("EXACTLY-ONCE VERIFIED: result identical (%d), outbox exactly %d, trace identical, reoffers=%d",
		killVal, killN, reoffers)
}

// --- shared: continuation polling + result read (process-level) --------------

func waitContinuationStatus(t *testing.T, e *procEnv, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = e.text(t, `SELECT status FROM continuation WHERE id=$1`, id)
		if last == want {
			return
		}
		if last == "failed" && want != "failed" {
			t.Fatalf("continuation %s failed while waiting for %q", id, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("continuation %s did not reach %q within %s (last=%q)", id, want, timeout, last)
}

func loadContinuationResult(t *testing.T, e *procEnv, id string) int64 {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	v, ok, err := cfr.LoadResult(context.Background(), conn, id)
	if err != nil || !ok {
		t.Fatalf("load result %s: ok=%v err=%v", id, ok, err)
	}
	return int64(v.N)
}
