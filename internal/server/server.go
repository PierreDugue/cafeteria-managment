// Package server wires the HTTP routes of the app.
package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/importer"
	"cafeteria-management/internal/store"
	"cafeteria-management/web"
)

// pageNames are the templates in web/templates rendered inside layout.html.
var pageNames = []string{"home", "import", "import_preview", "children", "compose"}

// Server serves the web UI.
type Server struct {
	version string
	store   *store.Store
	i18n    *i18n.Bundle
	pages   map[string]*template.Template
	pending *pendingImports
	handler http.Handler
}

// New builds the server. st must stay open while the server runs.
func New(version string, st *store.Store) (*Server, error) {
	bundle, err := i18n.Load(web.FS)
	if err != nil {
		return nil, err
	}
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}

	s := &Server{
		version: version,
		store:   st,
		i18n:    bundle,
		pages:   pages,
		pending: newPendingImports(30 * time.Minute),
	}

	mux := http.NewServeMux()
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /import", s.handleImportForm)
	mux.HandleFunc("POST /import/preview", s.handleImportPreview)
	mux.HandleFunc("POST /import/confirm", s.handleImportConfirm)
	mux.HandleFunc("GET /children", s.handleChildren)
	mux.HandleFunc("POST /compose", s.handleCompose)

	// Block requests from other websites open in the browser (CSRF) and
	// from other host names pointing at this machine (DNS rebinding).
	s.handler = localhostOnly(http.NewCrossOriginProtection().Handler(mux))
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		// issues packs the arguments of the "issues" sub-template.
		"issues": func(l i18n.Localizer, is []importer.Issue) any {
			return struct {
				L      i18n.Localizer
				Issues []importer.Issue
			}{l, is}
		},
	}

	pages := map[string]*template.Template{}
	for _, name := range pageNames {
		t, err := template.New(name).Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// view is the data given to every page.
type view struct {
	L       i18n.Localizer
	Version string
	Active  string // nav item to highlight
	Data    any
}

func (s *Server) localizer(r *http.Request) i18n.Localizer {
	return s.i18n.Localizer(s.i18n.Match(r.Header.Get("Accept-Language")))
}

// render executes a page into a buffer first, so a template error never
// sends a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page, active string, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown page %q", page))
		return
	}
	var buf bytes.Buffer
	v := view{L: s.localizer(r), Version: s.version, Active: active, Data: data}
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.serverError(w, r, fmt.Errorf("render %s: %w", page, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, s.localizer(r).T("error.internal"), http.StatusInternalServerError)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	totals, err := s.store.Totals(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	last, err := s.store.Imports(r.Context(), 1)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := struct {
		Totals     store.Totals
		LastImport *store.ImportRecord
	}{Totals: totals}
	if len(last) > 0 {
		data.LastImport = &last[0]
	}
	s.render(w, r, http.StatusOK, "home", "home", data)
}

// localhostOnly rejects requests whose Host is not a loopback name.
func localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch strings.ToLower(strings.Trim(host, "[]")) {
		case "127.0.0.1", "localhost", "::1":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	})
}
