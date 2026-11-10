// Package server wires the HTTP routes of the app.
package server

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"cafeteria-management/web"
)

type Server struct {
	version string
	tmpl    *template.Template
	mux     *http.ServeMux
}

func New(version string) (*Server, error) {
	tmpl, err := template.ParseFS(web.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	s := &Server{version: version, tmpl: tmpl, mux: http.NewServeMux()}
	if err := s.routes(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) routes() error {
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return err
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	data := struct{ Version string }{Version: s.version}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("render index", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
