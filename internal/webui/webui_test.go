package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testIndex = `<!doctype html><html><body><div id="app">SPA shell</div></body></html>`

// fixture is a minimal stand-in for a vite dist, including a nested asset
// directory and a file that must never be reachable through a traversal path.
func fixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte(testIndex)},
		"assets/app.js":     {Data: []byte("console.log(1)")},
		"assets/app.js.map": {Data: []byte("{}")},
		"assets/app.css":    {Data: []byte("body{}")},
		"assets/logo.svg":   {Data: []byte("<svg/>")},
		"favicon.ico":       {Data: []byte{0x00, 0x01, 0x02}},
		"secret.txt":        {Data: []byte("top secret")},
	}
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// --- placeholder / embedded build ---

// TestEmbeddedPlaceholderIsCommitted pins the acceptance criterion that a fresh
// clone with no pnpm build still has an embeddable index.html.
func TestEmbeddedPlaceholderIsCommitted(t *testing.T) {
	b, err := fs.ReadFile(FS(), indexFile)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", indexFile, err)
	}
	if len(b) == 0 || !bytes.Contains(bytes.ToLower(b), []byte("<html")) {
		t.Fatalf("embedded %s is not a non-empty HTML document: %q", indexFile, b)
	}
}

func TestEmbeddedHandlerServesIndexAtRoot(t *testing.T) {
	rec := do(Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Errorf("GET / body is not HTML: %q", rec.Body.String())
	}
}

// --- content types ---

func TestContentTypes(t *testing.T) {
	h := NewHandler(fixture())
	cases := []struct {
		path string
		want string
	}{
		{"/", "text/html; charset=utf-8"},
		{"/assets/app.js", "text/javascript; charset=utf-8"},
		{"/assets/app.css", "text/css; charset=utf-8"},
		{"/assets/app.js.map", "application/json"},
		{"/assets/logo.svg", "image/svg+xml"},
		{"/favicon.ico", "image/x-icon"},
	}
	for _, tc := range cases {
		rec := do(h, http.MethodGet, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != tc.want {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.path, ct, tc.want)
		}
	}
}

// TestIndexHTMLRedirectsToRoot documents the stdlib file-server canonical form:
// the shell is reachable at "/" and a direct /index.html request is redirected
// there rather than served twice.
func TestIndexHTMLRedirectsToRoot(t *testing.T) {
	rec := do(NewHandler(fixture()), http.MethodGet, "/index.html")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /index.html = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "./" {
		t.Errorf("GET /index.html Location = %q, want %q", loc, "./")
	}
}

func TestContentTypeMapping(t *testing.T) {
	cases := map[string]string{
		"app.JS":    "text/javascript; charset=utf-8",
		"a.woff2":   "font/woff2",
		"index.htm": "text/html; charset=utf-8",
		"noext":     "",
		"a.unknown": "",
	}
	for name, want := range cases {
		if got := contentType(name); got != want {
			t.Errorf("contentType(%q) = %q, want %q", name, got, want)
		}
	}
}

// --- static serving and SPA fallback ---

func TestServesExistingFileNotFallback(t *testing.T) {
	rec := do(NewHandler(fixture()), http.MethodGet, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/app.js = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "console.log(1)" {
		t.Errorf("GET /assets/app.js body = %q, want the file contents", got)
	}
}

func TestUnknownNonAPIPathFallsBackToIndex(t *testing.T) {
	h := NewHandler(fixture())
	for _, target := range []string{"/boards/abc", "/boards/abc/cards/1", "/deep/route/", "/deep/route?x=1"} {
		rec := do(h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (SPA fallback)", target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", target, ct)
		}
		if rec.Body.String() != testIndex {
			t.Errorf("GET %s body = %q, want the index shell", target, rec.Body.String())
		}
	}
}

func TestDirectoriesAreNotListed(t *testing.T) {
	h := NewHandler(fixture())
	for _, target := range []string{"/assets", "/assets/"} {
		rec := do(h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (fallback)", target, rec.Code)
			continue
		}
		if rec.Body.String() != testIndex {
			t.Errorf("GET %s body = %q, want index (no directory listing)", target, rec.Body.String())
		}
	}
}

// --- backend precedence ---

func TestAPIPathsAreDeclined(t *testing.T) {
	h := NewHandler(fixture())
	for _, target := range []string{"/api/unknown", "/api/boards/1", "/api", "/mcp/anything"} {
		rec := do(h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "SPA shell") {
			t.Errorf("GET %s served the UI shell; backend paths must be declined", target)
		}
	}
}

// TestMountedAtRootKeepsBackendPrecedence is the contract PB-94 relies on:
// mounting the UI handler at "/" must not shadow registered API/MCP routes nor
// change the 404 an unregistered /api/* path produces.
func TestMountedAtRootKeepsBackendPrecedence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("GET /mcp", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("mcp"))
	}))
	mux.Handle("/", NewHandler(fixture()))

	cases := []struct {
		target string
		code   int
		body   string
	}{
		{"/api/healthz", http.StatusOK, `{"ok":true}`},
		{"/mcp", http.StatusOK, "mcp"},
		{"/api/nope", http.StatusNotFound, "404 page not found"}, // ServeMux default shape
		{"/boards/abc", http.StatusOK, testIndex},
		{"/", http.StatusOK, testIndex},
	}
	for _, tc := range cases {
		rec := do(mux, http.MethodGet, tc.target)
		if rec.Code != tc.code {
			t.Errorf("GET %s = %d, want %d", tc.target, rec.Code, tc.code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("GET %s body = %q, want it to contain %q", tc.target, rec.Body.String(), tc.body)
		}
	}
}

func TestIsAPIPath(t *testing.T) {
	api := []string{"/api", "/api/", "/api/boards", "/api/boards/1", "/mcp", "/mcp/", "/mcp/x"}
	for _, p := range api {
		if !IsAPIPath(p) {
			t.Errorf("IsAPIPath(%q) = false, want true", p)
		}
	}
	ui := []string{"/", "/index.html", "/boards", "/boards/1", "/apix", "/API/x", "/ui/api", "/mcphealth", "/assets/app.js"}
	for _, p := range ui {
		if IsAPIPath(p) {
			t.Errorf("IsAPIPath(%q) = true, want false", p)
		}
	}
}

// --- traversal ---

func TestRegularFileRejectsTraversal(t *testing.T) {
	f := fixture()
	for _, p := range []string{"/../secret.txt", "/./secret.txt", "/a/../../secret.txt", "/..", "/assets/../../secret.txt", "/..%2Fsecret.txt"} {
		if name, ok := regularFile(f, p); ok {
			t.Errorf("regularFile(%q) = (%q, true), want (\"\", false)", p, name)
		}
	}
}

func TestTraversalRequestsNeverLeakFiles(t *testing.T) {
	h := NewHandler(fixture())
	targets := []string{
		"/../secret.txt",
		"/./secret.txt",
		"/assets/../../secret.txt",
		"/..%2Fsecret.txt",
		"//secret.txt",
	}
	for _, target := range targets {
		rec := do(h, http.MethodGet, target)
		if strings.Contains(rec.Body.String(), "top secret") {
			t.Errorf("GET %s leaked the traversal target: %q", target, rec.Body.String())
		}
		if rec.Code == http.StatusOK && rec.Body.String() != testIndex {
			t.Errorf("GET %s = 200 with body %q, want the index shell only", target, rec.Body.String())
		}
	}
}
