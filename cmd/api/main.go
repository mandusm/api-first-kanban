// Command api is the api-first-kanban HTTP API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/config"
	"api-first-kanban/internal/events"
	"api-first-kanban/internal/httpapi"
	"api-first-kanban/internal/mcp"
	"api-first-kanban/internal/service"
	"api-first-kanban/internal/store"
	"api-first-kanban/internal/webui"
)

func main() {
	cfg, help, err := parseFlags(os.Args[1:], config.Load(), os.Stdout, os.Stderr)
	if err != nil {
		// flag has already printed the error and usage to stderr.
		os.Exit(2)
	}
	if help {
		// -h/--help: usage already written to stdout. Success, exit 0.
		os.Exit(0)
	}
	if err := run(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// newHandler builds the server's HTTP surface: the REST API and the MCP
// endpoint keep their exact routes and error shapes, and every other path is
// served by the embedded web UI (SPA fallback) on the same loopback port.
func newHandler(svc *service.Service, hub *events.Hub) http.Handler {
	api := http.NewServeMux()
	httpapi.Register(api, svc, hub)
	mcp.Register(api, svc)

	ui := webui.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBackendRequest(r) {
			api.ServeHTTP(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	})
}

// isBackendRequest reports whether the request belongs to the REST API or the
// MCP endpoint and must be answered by the API mux untouched. The UI is not
// mounted as a plain "/" pattern because a catch-all match beats Go's
// 405 method-mismatch candidate, which would turn e.g. POST /api/healthz into
// the SPA shell instead of today's 405. The split is made here on
// webui.IsAPIPath; both the raw path and its cleaned form are checked so
// ServeMux's clean-path redirects for backend URLs are preserved, and a
// non-absolute request target (e.g. "OPTIONS *") stays with the API mux.
func isBackendRequest(r *http.Request) bool {
	p := r.URL.Path
	return !strings.HasPrefix(p, "/") || webui.IsAPIPath(p) || webui.IsAPIPath(path.Clean(p))
}

// Shutdown ordering constants. listenerStopGrace bounds how long the events
// listener is given to release its pooled Postgres connection after its
// context is cancelled; shutdownTimeout bounds the HTTP drain.
const (
	listenerStopGrace = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// startEventListener runs the events hub LISTEN loop on a cancellable child of
// ctx and returns a stop function that cancels it and waits (bounded) for the
// listener to release its pooled Postgres connection.
//
// The process depends on the ordering this enforces: stop accepting
// (srv.Shutdown) -> stop the hub (the returned function) -> pool.Close.
// hub.Listen holds an acquired pool connection for the whole process lifetime,
// and pgxpool.Close blocks until every acquired connection is returned, so
// calling pool.Close without stopping the hub first wedges the process
// forever. stop MUST therefore run before pool.Close (in run it is deferred
// after pool.Close, and defers run LIFO).
func startEventListener(ctx context.Context, hub *events.Hub, pool *pgxpool.Pool) func() {
	hubCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := hub.Listen(hubCtx, pool); err != nil {
			slog.Error("event listener stopped", "err", err)
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(listenerStopGrace):
			slog.Warn("event listener did not stop within grace period")
		}
	}
}

func run(cfg config.Config) error {
	ctx := context.Background()

	// No auth in v1: never expose the API beyond loopback.
	if !config.IsLoopbackAddr(cfg.Addr) {
		return fmt.Errorf("refusing to listen on non-loopback address %q (v1 has no authentication; set -addr, or %s, to a loopback host)", cfg.Addr, config.EnvAddr)
	}

	// Migrate up at startup (dev DX).
	if err := store.MigrateUp(ctx, cfg.DatabaseURL); err != nil {
		return err
	}

	pool, err := store.OpenPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	hub := events.NewHub()
	// Registered after defer pool.Close() so it runs FIRST (defers are LIFO):
	// the hub must release its LISTEN connection before the pool is closed, on
	// every return path (signal shutdown, bind failure, any later error).
	defer startEventListener(ctx, hub, pool)()

	svc := service.New(pool)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           newHandler(svc, hub),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1)

	slog.Info("listening", "addr", cfg.Addr, "version", Version)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-shutdown:
		slog.Info("shutting down", "signal", sig.String())
		// Restore the default disposition now: a second SIGINT/SIGTERM must
		// kill immediately instead of being swallowed by the buffered channel
		// (a wedged shutdown has to stay killable without SIGKILL).
		signal.Stop(shutdown)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
		return nil
	}
}
