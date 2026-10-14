package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/config"
	"api-first-kanban/internal/events"
	"api-first-kanban/internal/testutil"
)

// PB-98 regression tests for the shutdown/exit hang.
//
// Root cause (confirmed against pgx v5.11.0 / puddle v2.2.2): hub.Listen holds
// an acquired pool connection for the whole process lifetime, and
// (*pgxpool.Pool).Close -> (*puddle.Pool).Close waits (destructWG) for every
// acquired connection to be released. Running Listen on an uncancellable
// context and then calling pool.Close blocks forever, so neither signal
// shutdown nor the bind-failure error path could exit.
//
// The process-level tests below only surface the bug through the real binary
// entrypoint; the two unit tests pin the ordering contract (cancel the hub
// before Close) deterministically.

// shutdownHelperEnv marks a re-exec of the test binary as the server process:
// TestMain calls main() instead of running the test suite. This exercises the
// real signal handling and exit paths without shelling out to `go build`.
const shutdownHelperEnv = "KANBAN_SHUTDOWN_HELPER"

// promptExitBudget is the acceptance bound for a full process exit after a
// signal (or on the bind-failure path). The pre-fix hang exceeded two minutes,
// so this bound comfortably distinguishes fixed from broken.
const promptExitBudget = 2 * time.Second

// TestMain doubles as the entrypoint for the helper server processes spawned
// by the process-level tests.
func TestMain(m *testing.M) {
	if os.Getenv(shutdownHelperEnv) == "1" {
		main() // real run(): config, migrations, pool, hub, signal handling.
		return
	}
	os.Exit(m.Run())
}

// waitForAcquiredConn blocks until the hub's LISTEN goroutine holds a pooled
// connection, so a subsequent Close is guaranteed to be exposed to the hang.
func waitForAcquiredConn(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pool.Stat().AcquiredConns() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("event listener never acquired a pool connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStartEventListenerReleasesConnBeforePoolClose pins the fix's ordering:
// after stop() returns, pool.Close must complete promptly rather than block on
// the hub's acquired LISTEN connection.
func TestStartEventListenerReleasesConnBeforePoolClose(t *testing.T) {
	pool := testutil.OpenPool(t)
	hub := events.NewHub()
	stop := startEventListener(context.Background(), hub, pool)
	waitForAcquiredConn(t, pool)

	stop()

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("pool.Close blocked after the listener was stopped")
	}
}

// TestPoolCloseBlocksUntilListenerStopped documents the hazard the fix avoids:
// while the listener still holds its connection, pool.Close does NOT return.
// This fails loudly if a future pgx/puddle upgrade changes the semantics the
// shutdown ordering relies on.
func TestPoolCloseBlocksUntilListenerStopped(t *testing.T) {
	pool := testutil.OpenPool(t)
	hub := events.NewHub()
	stop := startEventListener(context.Background(), hub, pool)
	waitForAcquiredConn(t, pool)

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("pool.Close returned while the listener still held its connection; " +
			"the shutdown ordering assumption (and this test) need revisiting")
	case <-time.After(500 * time.Millisecond):
	}

	stop() // release the connection so the pending Close can finish
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("pool.Close still blocked after the listener was stopped")
	}
}

// freeAddr reserves an ephemeral loopback port and releases it immediately.
// Tests stay off the fixed dev/e2e ports so parallel lanes cannot collide.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

// startServerProcess re-execs the test binary as the server against the test
// database, returning the running command, its combined output buffer, and a
// kill-on-cleanup guarantee so no server process can leak.
func startServerProcess(t *testing.T, addr string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	// Bootstrap (and skip/fail on) the test DB exactly as the other tests do,
	// then point the child at the same database.
	testutil.OpenPool(t)
	dsn := config.Load().TestDatabaseURL

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		shutdownHelperEnv+"=1",
		"KANBAN_ADDR="+addr,
		"KANBAN_DATABASE_URL="+dsn,
	)
	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server process: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	return cmd, out
}

// waitHealthy polls /api/healthz until the server answers 200.
func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := "http://" + addr + "/api/healthz"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(url)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s never became healthy", addr)
}

// waitExited waits up to budget for the process to exit. On timeout it kills
// the process (so the test cannot leak a server) and reports the elapsed
// budget plus the collected output.
func waitExited(t *testing.T, cmd *exec.Cmd, out *bytes.Buffer, budget time.Duration) (time.Duration, bool) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	start := time.Now()
	select {
	case <-done:
		return time.Since(start), true
	case <-time.After(budget):
		_ = cmd.Process.Kill()
		<-done
		t.Logf("server output:\n%s", out.String())
		return budget, false
	}
}

// TestShutdownSignalExitsPromptly sends SIGINT then SIGTERM (separate runs) and
// requires a full process exit well within promptExitBudget.
func TestShutdownSignalExitsPromptly(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			addr := freeAddr(t)
			cmd, out := startServerProcess(t, addr)
			waitHealthy(t, addr)

			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signal %v: %v", sig, err)
			}
			elapsed, exited := waitExited(t, cmd, out, promptExitBudget)
			if !exited {
				t.Fatalf("process did not exit within %s after %v (still alive — shutdown hang)", promptExitBudget, sig)
			}
			if code := cmd.ProcessState.ExitCode(); code != 0 {
				t.Fatalf("exit code = %d after %v, want 0; output:\n%s", code, sig, out.String())
			}
			t.Logf("%v: exited in %s", sig, elapsed)

			// The listener port must be free once the process is gone.
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("port %s not released after exit: %v", addr, err)
			}
			_ = ln.Close()
		})
	}
}

// TestBindFailureExitsPromptlyWithError starts a second server on an address
// already held by a listener; the bind error must surface and the process must
// exit promptly instead of hanging in cleanup.
func TestBindFailureExitsPromptlyWithError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	cmd, out := startServerProcess(t, addr)
	elapsed, exited := waitExited(t, cmd, out, promptExitBudget)
	if !exited {
		t.Fatalf("bind-failure process did not exit within %s (hang); output:\n%s", promptExitBudget, out.String())
	}
	if code := cmd.ProcessState.ExitCode(); code == 0 {
		t.Fatalf("exit code = 0 on bind failure, want non-zero; output:\n%s", out.String())
	}
	if got := out.String(); !strings.Contains(got, "address already in use") {
		t.Fatalf("bind error missing from output; want 'address already in use', got:\n%s", got)
	}
	t.Logf("bind failure: exited in %s with code %d", elapsed, cmd.ProcessState.ExitCode())
}
