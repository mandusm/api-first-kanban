#!/usr/bin/env bash
# Isolated e2e stack (PB-68): an API instance backed by the kanban_test
# database on :18081, plus a Vite dev server on :5175 whose /api proxy points
# at it. The fe2 suites then run with FE2_API/APP_URL/FE2_DB overrides so test
# writes never land in the live kanban DB (see web/e2e/README.md).
#
# Lifecycle guarantees (PB-68 review): `start` builds and launches both
# processes detached (own session via setsid), records the REAL PIDs, and
# returns promptly once both ports answer. `stop` SIGTERMs each pid, waits,
# SIGKILLs on timeout, then FAILS unless both ports are actually free — a
# stale orphan can never silently pass a later `start` (start also refuses to
# run while either port is occupied). An INT/TERM/EXIT trap runs the same
# shutdown when start itself is interrupted mid-way.
#
# Usage:
#   scripts/e2e-test-stack.sh start   # build+start API(:18081) and Vite(:5175)
#   scripts/e2e-test-stack.sh stop    # stop both, assert ports free
set -euo pipefail
cd "$(dirname "$0")/.."

API_PORT=18081
VITE_PORT=5175
API_URL="http://127.0.0.1:${API_PORT}"
TEST_DSN="postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable"
API_BIN=/tmp/kan-e2e-api
API_LOG=/tmp/kan-e2e-api.log
VITE_LOG=/tmp/kan-e2e-vite.log
API_PID=/tmp/fe2-e2e-api.pid   # fe2-sse.mjs bounces this pid file (FE2_API_PID)
VITE_PID=/tmp/kan-e2e-vite.pid

port_busy() { ss -ltn "sport = :$1" 2>/dev/null | grep -q LISTEN; }

wait_up() { # url name
  for _ in $(seq 1 60); do
    if curl -sf "$1" >/dev/null 2>&1; then echo "$2 up"; return 0; fi
    sleep 0.5
  done
  echo "$2 failed to become ready" >&2
  return 1
}

stop_one() { # pidfile name
  local pf="$1" name="$2" pid
  if [ -f "$pf" ]; then
    pid="$(cat "$pf" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      # Whole process group (setsid): pnpm/vite style trees die together.
      kill -TERM -- "-$pid" "$pid" 2>/dev/null || true
      for _ in $(seq 1 10); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.5
      done
      if kill -0 "$pid" 2>/dev/null; then
        echo "  $name: SIGTERM timed out, SIGKILL" >&2
        kill -KILL -- "-$pid" "$pid" 2>/dev/null || true
      fi
    fi
    rm -f "$pf"
  fi
}

do_stop() {
  stop_one "$API_PID" API
  stop_one "$VITE_PID" Vite
  # Fallback: a process may be listening without a pid file (e.g. killed
  # mid-start before Vite's pid was recorded). Kill it by listener pid.
  for p in "$API_PORT" "$VITE_PORT"; do
    if port_busy "$p"; then
      for lpid in $(ss -ltnp "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | sort -u); do
        kill -TERM -- "-$lpid" "$lpid" 2>/dev/null || true
      done
      sleep 1
      for lpid in $(ss -ltnp "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | sort -u); do
        kill -KILL -- "-$lpid" "$lpid" 2>/dev/null || true
      done
    fi
  done
  # Assert reality: both ports must actually be free, or stop failed.
  local bad=0
  if port_busy "$API_PORT"; then echo "ERROR: port $API_PORT still listening" >&2; bad=1; fi
  if port_busy "$VITE_PORT"; then echo "ERROR: port $VITE_PORT still listening" >&2; bad=1; fi
  if [ "$bad" -eq 0 ]; then
    echo "e2e test stack stopped (ports $API_PORT/$VITE_PORT free)"
  fi
  return "$bad"
}

case "${1:-}" in
  start)
    for p in "$API_PORT" "$VITE_PORT"; do
      if port_busy "$p"; then
        echo "ERROR: port $p already in use (stale stack? run '$0 stop' or kill the orphan first)" >&2
        exit 1
      fi
    done
    go build -o "$API_BIN" ./cmd/api
    # From here on, an interrupt must not leave half a stack behind. (Installed
    # only after the port guard: a refused start must not tear down a healthy
    # stack that is already running.)
    # Interrupt mid-start → tear down the partial stack and abort (exit, so
    # bash does not resume the start flow after the handler).
    trap 'do_stop 2>/dev/null; exit 1' INT TERM
    setsid env KANBAN_DATABASE_URL="$TEST_DSN" KANBAN_ADDR="127.0.0.1:${API_PORT}" \
      "$API_BIN" >"$API_LOG" 2>&1 < /dev/null &
    echo $! > "$API_PID"
    disown || true
    wait_up "$API_URL/api/healthz" "API(:${API_PORT}, kanban_test)" || {
      do_stop || true
      trap - INT TERM
      exit 1
    }
    # Vite: exec'd setsid in a detached subshell — the subshell's fds are fully
    # redirected so no pipe fd is ever inherited (a piped `start | cat` sees
    # EOF and terminates promptly), and the pid is recorded immediately so an
    # interrupt between launch and readiness can still tear it down.
    ( cd web && exec setsid env KANBAN_API_TARGET="$API_URL" \
      ./node_modules/.bin/vite --port "$VITE_PORT" --strictPort \
      >"$VITE_LOG" 2>&1 < /dev/null ) >/dev/null 2>&1 < /dev/null &
    vite_pid=$!
    echo "$vite_pid" > "$VITE_PID"
    disown || true
    for _ in $(seq 1 60); do
      if curl -sf "http://127.0.0.1:${VITE_PORT}/" >/dev/null 2>&1; then break; fi
      sleep 0.5
    done
    # Confirm the recorded pid really owns the listener (defensive; the
    # setsid --fork-free exec chain keeps $! stable).
    listen_pid="$(ss -ltnp "sport = :$VITE_PORT" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1 || true)"
    if [ -n "$listen_pid" ] && [ "$listen_pid" != "$vite_pid" ]; then
      vite_pid="$listen_pid"
      echo "$vite_pid" > "$VITE_PID"
    fi
    if [ -z "$listen_pid" ]; then
      echo "Vite(:${VITE_PORT}) failed to become ready — see $VITE_LOG" >&2
      do_stop || true
      trap - INT TERM
      exit 1
    fi
    echo "Vite(:${VITE_PORT}) up (pid $vite_pid)"
    trap - INT TERM   # stack is up and detached — do NOT tear it down on script exit
    ;;
  stop)
    do_stop
    ;;
  *)
    echo "usage: $0 {start|stop}" >&2
    exit 2
    ;;
esac
