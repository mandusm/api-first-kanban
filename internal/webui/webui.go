// Package webui serves the api-first-kanban single-page web UI from files
// embedded in the Go binary, so a release artifact is one executable with no
// separate node/vite runtime (milestone PB-89).
//
// The embedded directory (dist) holds the vite build output. A placeholder
// dist/index.html is committed so the repository compiles and tests pass on a
// fresh clone without node_modules or pnpm; the release build copies the real
// web/dist over it (PB-95). Nothing else under dist is committed.
//
// Backend routes keep strict precedence: mount Handler at "/" and the handler
// itself declines backend paths (see IsAPIPath), so an unregistered /api/*
// request still gets the API's original 404 instead of the SPA shell.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// distDir is the build-output directory embedded into the binary.
const distDir = "dist"

// indexFile is the SPA entry point served at "/" and as the fallback for
// client-side routes.
const indexFile = "index.html"

//go:embed all:dist
var embedded embed.FS

// FS returns the embedded web build, rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, distDir)
	if err != nil {
		// Unreachable: distDir is the compile-time constant embedded above.
		panic("webui: embedded dist missing: " + err.Error())
	}
	return sub
}

// Handler serves the embedded web UI.
func Handler() http.Handler { return NewHandler(FS()) }

// NewHandler serves fsys as the web UI root. Handler uses the embedded build;
// NewHandler is exported so tests (and embedders with their own build) can
// supply a filesystem.
//
// Semantics:
//   - an existing regular file is served with an explicit content type;
//   - "/" and any other non-backend path that is not a regular file fall back
//     to index.html (SPA client-side routing);
//   - backend paths (IsAPIPath) are declined with the same plain 404 the
//     http.ServeMux default produces, leaving API routing and error shape
//     untouched;
//   - directories are never listed.
func NewHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		name, ok := regularFile(fsys, r.URL.Path)
		if !ok {
			// SPA fallback: serve the app shell for client-side routes. "/"
			// makes the file server return index.html itself; pointing at
			// "/index.html" would instead trigger its canonical redirect.
			w.Header().Set("Content-Type", contentType(indexFile))
			files.ServeHTTP(w, rewritePath(r, "/"))
			return
		}
		if ct := contentType(name); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
}

// IsAPIPath reports whether p belongs to the backend: the REST API under /api
// or the MCP endpoint at /mcp. The UI handler never answers these paths, so a
// caller can mount Handler at "/" while preserving exact API/MCP precedence.
func IsAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/mcp" || strings.HasPrefix(p, "/mcp/")
}

// regularFile maps a request path to a servable file name and reports whether
// it exists in fsys as a regular (non-directory) file. "/" maps to index.html.
// Malformed or traversal paths (fs.ValidPath rejects "." and ".." elements)
// are never stat'ed.
func regularFile(fsys fs.FS, urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, "/")
	if name == "" {
		name = indexFile
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	info, err := fs.Stat(fsys, name)
	if err != nil || info.IsDir() {
		return "", false
	}
	return name, true
}

// rewritePath returns a shallow copy of r with the request path replaced, so
// the caller's request is not mutated.
func rewritePath(r *http.Request, p string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.URL.Path = p
	return r2
}

// contentType returns the explicit media type for a web asset name, or "" when
// the extension is unknown. The table is deliberate rather than delegated to
// mime.TypeByExtension, which consults host files such as /etc/mime.types and
// can therefore differ between machines; a blank return lets the file server
// sniff exactly as it always has.
func contentType(name string) string {
	return contentTypes[strings.ToLower(path.Ext(name))]
}

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".wasm":        "application/wasm",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}
