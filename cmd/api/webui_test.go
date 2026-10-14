package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/service"
	"api-first-kanban/internal/testutil"
)

// spaMarker identifies the embedded app shell in a response body. It appears in
// both the committed placeholder and a real vite build, and in no API/MCP
// response.
const spaMarker = `id="app"`

// unifiedHandler is the production handler under test. Its API/MCP requests
// never reach the service (they are unmatched or method-mismatched), so a nil
// pool is safe here; the one test that exercises a known route builds its own
// DB-backed handler.
func unifiedHandler() http.Handler { return newHandler(service.New(nil), events.NewHub()) }

// prePB94Handler is the handler exactly as it was before the UI mount: REST +
// MCP only. It is the reference for byte-stability comparisons.
func prePB94Handler() http.Handler { return testutil.NewMuxWithMCP(nil, events.NewHub()) }

type httpResp struct {
	code     int
	ctype    string
	nosniff  string
	allow    string
	location string
	body     string
}

func capture(h http.Handler, method, target string) httpResp {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return snapshot(rec)
}

// captureRaw bypasses httptest's target parsing, which would rewrite a
// protocol-relative-looking "//api/x" into a host + path.
func captureRaw(h http.Handler, method, target string) httpResp {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, &http.Request{
		Method:     method,
		URL:        &url.URL{Path: target},
		RequestURI: target,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{},
	})
	return snapshot(rec)
}

func snapshot(rec *httptest.ResponseRecorder) httpResp {
	return httpResp{
		code:     rec.Code,
		ctype:    rec.Header().Get("Content-Type"),
		nosniff:  rec.Header().Get("X-Content-Type-Options"),
		allow:    rec.Header().Get("Allow"),
		location: rec.Header().Get("Location"),
		body:     rec.Body.String(),
	}
}

// --- the UI itself ---

func TestWebUIIndexServedAtRoot(t *testing.T) {
	r := capture(unifiedHandler(), http.MethodGet, "/")
	if r.code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", r.code)
	}
	if !strings.HasPrefix(r.ctype, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", r.ctype)
	}
	if !strings.Contains(r.body, spaMarker) {
		t.Errorf("GET / body does not look like the app shell: %q", r.body)
	}
}

func TestUnknownNonAPIPathServesSPA(t *testing.T) {
	h := unifiedHandler()
	for _, target := range []string{"/boards/abc", "/boards/abc/cards/1", "/some/deep/route?x=1"} {
		r := capture(h, http.MethodGet, target)
		if r.code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (SPA fallback)", target, r.code)
			continue
		}
		if !strings.HasPrefix(r.ctype, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", target, r.ctype)
		}
		if !strings.Contains(r.body, spaMarker) {
			t.Errorf("GET %s body is not the app shell: %q", target, r.body)
		}
	}
}

// --- backend precedence and byte-stability ---

// TestBackendResponsesByteStable pins that mounting the UI changed nothing for
// API/MCP paths: for every probe the unified handler produces exactly the same
// status, headers and body as the pre-PB-94 REST+MCP mux, and never the SPA.
// The method-mismatch rows cover the hazard that rules out a plain "/" mount: a
// catch-all match would beat ServeMux's 405 candidate.
func TestBackendResponsesByteStable(t *testing.T) {
	before := prePB94Handler()
	after := unifiedHandler()

	cases := []struct{ method, target string }{
		{http.MethodGet, "/api/definitely-not-a-route"},    // unmatched -> default 404
		{http.MethodDelete, "/api/definitely-not-a-route"}, // unmatched, non-GET -> default 404
		{http.MethodPost, "/api/definitely-not-a-route"},   // unmatched, non-GET -> default 404
		{http.MethodPost, "/api/healthz"},                  // known GET route, wrong method -> 405
		{http.MethodDelete, "/api/workspaces"},             // known POST route, wrong method -> 405
		{http.MethodPatch, "/api/workspaces"},              // known GET/POST route, wrong method -> 405
		{http.MethodGet, "/mcp/not-a-route"},               // stray MCP subpath -> default 404
		{http.MethodPost, "/mcp/not-a-route"},              // stray MCP subpath, non-GET -> default 404
		{http.MethodPut, "/mcp/not-a-route"},               // stray MCP subpath, non-GET -> default 404
	}

	for _, tc := range cases {
		got := capture(after, tc.method, tc.target)
		want := capture(before, tc.method, tc.target)
		if got != want {
			t.Errorf("%s %s changed vs pre-PB-94:\n got  %+v\n want %+v", tc.method, tc.target, got, want)
		}
		if strings.Contains(got.body, spaMarker) || strings.HasPrefix(got.ctype, "text/html") {
			t.Errorf("%s %s served the SPA shell (code=%d ctype=%q)", tc.method, tc.target, got.code, got.ctype)
		}
	}

	// The two shapes the brief calls out, asserted explicitly so a regression
	// message is self-explanatory.
	if got := capture(after, http.MethodGet, "/api/definitely-not-a-route"); got.code != http.StatusNotFound ||
		got.body != "404 page not found\n" || got.ctype != "text/plain; charset=utf-8" || got.nosniff != "nosniff" {
		t.Errorf("unmatched GET /api/* = %+v, want the ServeMux default 404 shape", got)
	}
	if got := capture(after, http.MethodPost, "/api/healthz"); got.code != http.StatusMethodNotAllowed || !strings.Contains(got.allow, "GET") {
		t.Errorf("POST /api/healthz = %+v, want 405 with Allow listing GET", got)
	}
}

// TestBackendCleanPathRedirectsPreserved pins that ServeMux clean-path behavior
// for backend URLs is untouched, including the duplicate-slash form that
// httptest would otherwise reparse as a host.
func TestBackendCleanPathRedirectsPreserved(t *testing.T) {
	before := prePB94Handler()
	after := unifiedHandler()
	for _, target := range []string{"//api/x", "/api/../boards", "/api/./boards", "//mcp/x"} {
		got := captureRaw(after, http.MethodGet, target)
		want := captureRaw(before, http.MethodGet, target)
		if got != want {
			t.Errorf("GET %s changed vs pre-PB-94:\n got  %+v\n want %+v", target, got, want)
		}
	}
}

// TestKnownAPIRouteUnchangedThroughUIHandler drives a real, registered route
// through the unified handler against the shared dev DB (skips when it is down,
// fails under KANBAN_TEST_STRICT=1 — same convention as the other cmd/api
// tests).
func TestKnownAPIRouteUnchangedThroughUIHandler(t *testing.T) {
	pool := testutil.OpenPool(t)
	h := newHandler(service.New(pool), events.NewHub())

	r := capture(h, http.MethodGet, "/api/healthz")
	if r.code != http.StatusOK {
		t.Fatalf("GET /api/healthz = %d, want 200; body=%s", r.code, r.body)
	}
	if !strings.HasPrefix(r.ctype, "application/json") {
		t.Errorf("GET /api/healthz Content-Type = %q, want application/json", r.ctype)
	}
	if !strings.Contains(r.body, `"status":"ok"`) {
		t.Errorf("GET /api/healthz body = %q, want the healthz JSON envelope", r.body)
	}
	if strings.Contains(r.body, spaMarker) {
		t.Errorf("GET /api/healthz served the SPA shell")
	}
}

// --- MCP ---

// TestMCPReachableThroughUnifiedHandler connects a real MCP client over HTTP to
// /mcp on the unified handler. initialize + tools/list never invoke the
// service, so no database is needed.
func TestMCPReachableThroughUnifiedHandler(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(unifiedHandler())
	t.Cleanup(srv.Close)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pb94-test", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp through unified handler: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	if ir := cs.InitializeResult(); ir == nil || ir.ServerInfo == nil || ir.ServerInfo.Name != "api-first-kanban" {
		t.Fatalf("initialize result = %+v, want server api-first-kanban", ir)
	}
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make(map[string]bool, len(res.Tools))
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_workspaces", "get_board", "create_item"} {
		if !names[want] {
			t.Errorf("/mcp tools/list missing %q", want)
		}
	}
}

// --- dispatcher unit ---

func TestIsBackendRequest(t *testing.T) {
	backend := []string{"/api", "/api/", "/api/boards", "/api/boards/1", "/mcp", "/mcp/", "/mcp/x",
		"//api/x", "/api/../boards", "/api/./boards", "//mcp/x", "/mcp/../x", "*", ""}
	for _, p := range backend {
		r := &http.Request{URL: &url.URL{Path: p}}
		if !isBackendRequest(r) {
			t.Errorf("isBackendRequest(%q) = false, want true", p)
		}
	}
	ui := []string{"/", "/index.html", "/boards", "/boards/1", "//boards", "/apix", "/API/x",
		"/ui/api", "/mcphealth", "/assets/app.js"}
	for _, p := range ui {
		r := &http.Request{URL: &url.URL{Path: p}}
		if isBackendRequest(r) {
			t.Errorf("isBackendRequest(%q) = true, want false", p)
		}
	}
}

// TestOptionsStarStaysWithAPI pins the non-absolute-target guard: "OPTIONS *"
// must keep ServeMux's 400 rather than becoming an SPA response.
func TestOptionsStarStaysWithAPI(t *testing.T) {
	r := captureRaw(unifiedHandler(), http.MethodOptions, "*")
	if r.code != http.StatusBadRequest {
		t.Errorf("OPTIONS * = %d, want 400 (ServeMux)", r.code)
	}
	if strings.Contains(r.body, spaMarker) {
		t.Errorf("OPTIONS * served the SPA shell: %q", r.body)
	}
}
