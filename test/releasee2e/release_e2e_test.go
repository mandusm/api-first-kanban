// Package releasee2e is the black-box smoke suite for the single-file release
// artifact built by `make release` (bin/kanban-server): one Go binary serving
// the embedded SPA, the REST API and SSE on one loopback port.
//
// It is deliberately a separate package from cmd/api: it never imports the
// server's internals, and cmd/api's tests own their own TestMain and process
// helpers (PB-98 adds those to package main). Each test owns its process and an
// ephemeral loopback port, and tears both down even on failure.
//
// The suite is gated. It skips unless KANBAN_RELEASE_BIN and
// KANBAN_TEST_DATABASE_URL are set — the `make test-release` target sets both —
// so `go test ./...` / `make test` stays green on a fresh clone with no
// node/pnpm (the vite build is the heavy step, kept out of `make test`).
package releasee2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	healthTimeout   = 20 * time.Second
	shutdownBudget  = 5 * time.Second
	portFreeTimeout = 3 * time.Second
	sseOpenTimeout  = 5 * time.Second
)

// --- test env / process harness ---

// releaseEnv resolves the gated inputs, skipping (never failing) when the suite
// is not explicitly requested.
func releaseEnv(t *testing.T) (bin, dsn string) {
	t.Helper()
	bin = os.Getenv("KANBAN_RELEASE_BIN")
	dsn = os.Getenv("KANBAN_TEST_DATABASE_URL")
	if bin == "" || dsn == "" {
		t.Skip("release e2e is gated: set KANBAN_RELEASE_BIN and KANBAN_TEST_DATABASE_URL (run `make test-release`)")
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("KANBAN_RELEASE_BIN=%q: %v (run `make test-release`)", bin, err)
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		t.Fatalf("KANBAN_RELEASE_BIN=%q is not an executable file", bin)
	}
	return bin, dsn
}

// freePort reserves an ephemeral loopback port. Never the shared e2e ports
// (:18080 live, :18081/:5175) — each test picks its own.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// portOccupied reports whether something still accepts connections on port.
func portOccupied(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

// lockedBuffer captures child stdout+stderr safely: exec writes from its own
// goroutine while the test reads on failure.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type testServer struct {
	cmd    *exec.Cmd
	base   string
	port   int
	out    *lockedBuffer
	done   chan error
	exited atomic.Bool
}

// startReleaseServer launches the built artifact on a fresh port against the
// test DB and blocks until /api/healthz answers. Cleanup SIGKILLs a survivor, so
// a failed test can never leak a process or a bound port.
func startReleaseServer(t *testing.T, bin, dsn string) *testServer {
	t.Helper()
	port := freePort(t)
	out := &lockedBuffer{}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("KANBAN_ADDR=127.0.0.1:%d", port),
		"KANBAN_DATABASE_URL="+dsn,
	)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start release binary: %v", err)
	}
	s := &testServer{
		cmd:  cmd,
		base: fmt.Sprintf("http://127.0.0.1:%d", port),
		port: port,
		out:  out,
		done: make(chan error, 1),
	}
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(s.kill)
	if !s.waitHealthy() {
		t.Fatalf("release binary never answered /api/healthz within %s; output:\n%s", healthTimeout, out.String())
	}
	return s
}

func (s *testServer) waitHealthy() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			s.exited.Store(true)
			return false
		default:
		}
		resp, err := client.Get(s.base + "/api/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// sigtermAndWait sends SIGTERM and waits at most budget for a full exit,
// returning the exit code (0 = clean) and whether it exited in time. A survivor
// is SIGKILLed so the cleanup remains self-cleaning.
func (s *testServer) sigtermAndWait(budget time.Duration) (time.Duration, int, bool) {
	start := time.Now()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return 0, -1, false
	}
	select {
	case err := <-s.done:
		s.exited.Store(true)
		code := 0
		var ee *exec.ExitError
		if err != nil {
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		return time.Since(start), code, true
	case <-time.After(budget):
		_ = s.cmd.Process.Kill()
		select {
		case <-s.done:
			s.exited.Store(true)
		case <-time.After(3 * time.Second):
		}
		return time.Since(start), -1, false
	}
}

func (s *testServer) kill() {
	if s.exited.Load() {
		return
	}
	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
		s.exited.Store(true)
	case <-time.After(3 * time.Second):
	}
}

func (s *testServer) waitPortFree() bool {
	deadline := time.Now().Add(portFreeTimeout)
	for time.Now().Before(deadline) {
		if !portOccupied(s.port) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// --- HTTP helpers ---

type apiResp struct {
	code  int
	ctype string
	body  []byte
}

func doReq(t *testing.T, method, url, body string) apiResp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return apiResp{code: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b}
}

// postJSON issues a JSON POST and returns the status plus the decoded envelope.
func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	r := doReq(t, http.MethodPost, url, body)
	var env map[string]any
	if err := json.Unmarshal(r.body, &env); err != nil {
		t.Fatalf("POST %s: decode %q: %v", url, r.body, err)
	}
	return r.code, env
}

// idOf extracts env[key].id, failing with the full envelope when absent.
func idOf(t *testing.T, env map[string]any, key string) string {
	t.Helper()
	obj, ok := env[key].(map[string]any)
	if !ok {
		t.Fatalf("response has no %q object: %v", key, env)
	}
	id, _ := obj["id"].(string)
	if id == "" {
		t.Fatalf("%s object has no id: %v", key, obj)
	}
	return id
}

var assetRefRe = regexp.MustCompile(`/assets/[A-Za-z0-9_.\-]+\.(?:js|css)`)

// --- SSE ---

type sseReader struct {
	events chan map[string]any
	body   io.ReadCloser
	once   sync.Once
}

// openSSE connects and waits for the handler's opening ": stream open" comment,
// which it writes after subscribing, before returning.
func openSSE(t *testing.T, url string) *sseReader {
	t.Helper()
	resp, err := http.Get(url) // no client timeout: the stream stays open
	if err != nil {
		t.Fatalf("open SSE %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("SSE %s = %d, want 200", url, resp.StatusCode)
	}
	s := &sseReader{events: make(chan map[string]any, 256), body: resp.Body}
	t.Cleanup(s.close)

	ready := make(chan struct{})
	var once sync.Once
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ": stream open"):
				once.Do(func() { close(ready) })
			case strings.HasPrefix(line, "data:"):
				var ev map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err == nil {
					select {
					case s.events <- ev:
					default:
					}
				}
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(sseOpenTimeout):
		t.Fatalf("SSE %s never sent the opening comment", url)
	}
	return s
}

// close disconnects the SSE client. srv.Shutdown waits for active requests, so
// a test that signals the server must drop this stream first; the hub keeps its
// own DB LISTEN connection either way, which is what the shutdown fix covers.
func (s *sseReader) close() {
	s.once.Do(func() { _ = s.body.Close() })
}

// waitFor drains events until match accepts one or d elapses (nil on timeout).
func (s *sseReader) waitFor(d time.Duration, match func(map[string]any) bool) map[string]any {
	deadline := time.After(d)
	for {
		select {
		case ev := <-s.events:
			if match(ev) {
				return ev
			}
		case <-deadline:
			return nil
		}
	}
}

// --- tests ---

// TestReleaseBinaryServesUIAPIAndSSEOnOnePort is the milestone's core claim:
// the artifact is self-contained — the real SPA (not the committed placeholder),
// its hashed assets, board CRUD and a live SSE event all come from the one
// process on the one port.
func TestReleaseBinaryServesUIAPIAndSSEOnOnePort(t *testing.T) {
	bin, dsn := releaseEnv(t)
	s := startReleaseServer(t, bin, dsn)

	// 1. GET / serves the REAL vite build, and its hashed asset is embedded too.
	index := doReq(t, http.MethodGet, s.base+"/", "")
	if index.code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", index.code)
	}
	if !strings.HasPrefix(index.ctype, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", index.ctype)
	}
	if strings.Contains(string(index.body), "frontend placeholder") {
		t.Fatalf("GET / served the committed placeholder — the release binary must embed the real vite build")
	}
	asset := assetRefRe.FindString(string(index.body))
	if asset == "" {
		t.Fatalf("built index has no /assets/*.js|css reference (is the real dist embedded?): %s", index.body)
	}
	a := doReq(t, http.MethodGet, s.base+asset, "")
	if a.code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200 (hashed asset embedded)", asset, a.code)
	}
	want := "javascript"
	if strings.HasSuffix(asset, ".css") {
		want = "css"
	}
	if !strings.Contains(a.ctype, want) {
		t.Errorf("GET %s Content-Type = %q, want it to contain %q", asset, a.ctype, want)
	}

	// 2. Unknown non-API path -> SPA shell (same port).
	spa := doReq(t, http.MethodGet, s.base+"/boards/does-not-exist", "")
	if spa.code != http.StatusOK || !strings.Contains(string(spa.body), `id="app"`) {
		t.Errorf("GET /boards/does-not-exist = %d, want 200 with the SPA shell: %s", spa.code, spa.body)
	}

	// 3. Full board CRUD through /api on the same port.
	tag := fmt.Sprintf("rel-%d", time.Now().UnixNano())
	code, env := postJSON(t, s.base+"/api/workspaces", fmt.Sprintf(`{"name":%q,"agent":"release-e2e"}`, tag))
	if code != http.StatusCreated {
		t.Fatalf("create workspace = %d %v", code, env)
	}
	wsID := idOf(t, env, "workspace")

	code, env = postJSON(t, s.base+"/api/workspaces/"+wsID+"/boards", fmt.Sprintf(`{"name":"board-%s","agent":"release-e2e"}`, tag))
	if code != http.StatusCreated {
		t.Fatalf("create board = %d %v", code, env)
	}
	boardID := idOf(t, env, "board")

	itemTitle := "item-" + tag
	code, env = postJSON(t, s.base+"/api/boards/"+boardID+"/items", fmt.Sprintf(`{"title":%q,"agent":"release-e2e"}`, itemTitle))
	if code != http.StatusCreated {
		t.Fatalf("create item = %d %v", code, env)
	}
	itemID := idOf(t, env, "item")

	snap := doReq(t, http.MethodGet, s.base+"/api/boards/"+boardID, "")
	if snap.code != http.StatusOK || !strings.Contains(string(snap.body), itemTitle) {
		t.Errorf("GET /api/boards/%s = %d, want 200 containing %q: %s", boardID, snap.code, itemTitle, snap.body)
	}
	got := doReq(t, http.MethodGet, s.base+"/api/items/"+itemID, "")
	if got.code != http.StatusOK || !strings.Contains(string(got.body), itemTitle) {
		t.Errorf("GET /api/items/%s = %d, want 200 containing %q: %s", itemID, got.code, itemTitle, got.body)
	}

	// 4. A live SSE event on the same port. The hub's LISTEN attach is async, so
	// retry the mutation until its event arrives (bounded).
	stream := openSSE(t, s.base+"/api/events")
	match := func(ev map[string]any) bool {
		return ev["type"] == "item.created" && ev["board_id"] == boardID
	}
	var ev map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for ev == nil && time.Now().Before(deadline) {
		code, env = postJSON(t, s.base+"/api/boards/"+boardID+"/items",
			fmt.Sprintf(`{"title":"sse-%s","agent":"release-e2e"}`, tag))
		if code != http.StatusCreated {
			t.Fatalf("create item (SSE trigger) = %d %v", code, env)
		}
		ev = stream.waitFor(1500*time.Millisecond, match)
	}
	if ev == nil {
		t.Fatalf("no item.created SSE event for board %s within the window; output:\n%s", boardID, s.out.String())
	}
}

// TestReleaseBinaryGracefulShutdownExitsPromptly sends SIGTERM after the SSE
// pipeline is provably live (so the events hub really holds its LISTEN
// connection) and requires a full exit with code 0 and a released port within
// shutdownBudget. This is the release-surface pin for the PB-98 shutdown hang.
func TestReleaseBinaryGracefulShutdownExitsPromptly(t *testing.T) {
	bin, dsn := releaseEnv(t)
	s := startReleaseServer(t, bin, dsn)
	proveEventsPipeline(t, s)

	d, code, ok := s.sigtermAndWait(shutdownBudget)
	if !ok {
		t.Fatalf("SIGTERM: process did not exit within %s (shutdown hang); output:\n%s", shutdownBudget, s.out.String())
	}
	if code != 0 {
		t.Fatalf("SIGTERM: exit code = %d, want 0; output:\n%s", code, s.out.String())
	}
	if !s.waitPortFree() {
		t.Errorf("SIGTERM: port %d still accepts connections %s after exit", s.port, portFreeTimeout)
	}
	t.Logf("SIGTERM: exited in %s (code 0), port %d released", d, s.port)
}

// proveEventsPipeline makes the events hub hold its LISTEN connection by
// pushing a workspace mutation and receiving its SSE event; it retries to ride
// out the hub's async attach.
func proveEventsPipeline(t *testing.T, s *testServer) {
	t.Helper()
	stream := openSSE(t, s.base+"/api/events")
	tag := fmt.Sprintf("rel-sse-%d", time.Now().UnixNano())
	matched := false
	deadline := time.Now().Add(10 * time.Second)
	for !matched && time.Now().Before(deadline) {
		code, env := postJSON(t, s.base+"/api/workspaces", fmt.Sprintf(`{"name":%q,"agent":"release-e2e"}`, tag))
		if code != http.StatusCreated {
			t.Fatalf("create workspace (SSE trigger) = %d %v", code, env)
		}
		matched = stream.waitFor(1500*time.Millisecond, func(ev map[string]any) bool {
			return ev["type"] == "workspace.created"
		}) != nil
	}
	if !matched {
		t.Fatalf("no workspace.created SSE event within the window; output:\n%s", s.out.String())
	}
	// Drop the client so graceful shutdown is not held open by an active SSE
	// request; the hub's LISTEN connection survives this and is the actual
	// shutdown hazard under test (PB-98).
	stream.close()
}
