package server

import (
	"errors"
	"log/slog"
	"net/http"

	"cafeteria-management/internal/store"
)

const maxListFormBytes = 1 << 20

type homeData struct {
	Lists  []store.List
	Error  string
	Notice string
}

type listFormData struct {
	ID      int64
	Name    string // the typed name
	Error   string
	MaxName int
}

type listDeleteData struct {
	List store.List
}

// handleHome shows one line per list; ?renamed=1 and ?deleted=1 show a
// notice after a change.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	loc := s.localizer(r)
	notice := ""
	switch {
	case r.URL.Query().Get("renamed") == "1":
		notice = loc.T("lists.renamed")
	case r.URL.Query().Get("deleted") == "1":
		notice = loc.T("lists.deleted")
	}
	s.renderHome(w, r, http.StatusOK, "", notice)
}

func (s *Server) renderHome(w http.ResponseWriter, r *http.Request, status int, errMsg, notice string) {
	lists, err := s.store.Lists(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "home", "home", homeData{Lists: lists, Error: errMsg, Notice: notice})
}

func (s *Server) listNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderHome(w, r, http.StatusNotFound, s.localizer(r).T("lists.error.not_found"), "")
}

// loadList reads the list of the {id} route. It returns ok=false when it has
// already answered (unknown list, or failure).
func (s *Server) loadList(w http.ResponseWriter, r *http.Request) (store.List, bool) {
	id, ok := parsePathID(r)
	if !ok {
		s.listNotFound(w, r)
		return store.List{}, false
	}
	l, err := s.store.List(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.listNotFound(w, r)
		return store.List{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return store.List{}, false
	}
	return l, true
}

func (s *Server) renderListRename(w http.ResponseWriter, r *http.Request, status int, data listFormData) {
	data.MaxName = maxNameLen
	s.render(w, r, status, "list_rename", "home", data)
}

// handleListRenameForm shows the rename form; nothing changes on GET.
func (s *Server) handleListRenameForm(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadList(w, r)
	if !ok {
		return
	}
	s.renderListRename(w, r, http.StatusOK, listFormData{ID: l.ID, Name: l.Name})
}

func (s *Server) handleListRename(w http.ResponseWriter, r *http.Request) {
	// The list must exist before its new name is judged (404 before 422).
	l, ok := s.loadList(w, r)
	if !ok {
		return
	}
	id := l.ID
	loc := s.localizer(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxListFormBytes)
	data := listFormData{ID: id}
	if err := r.ParseForm(); err != nil {
		data.Error = loc.T("children.error.too_large")
		s.renderListRename(w, r, http.StatusUnprocessableEntity, data)
		return
	}
	data.Name = r.PostForm.Get("name")
	name, msg := validateListName(loc, data.Name)
	if msg != "" {
		data.Error = msg
		s.renderListRename(w, r, http.StatusUnprocessableEntity, data)
		return
	}

	err := s.store.RenameList(r.Context(), id, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.listNotFound(w, r)
	case errors.Is(err, store.ErrNameTaken):
		data.Error = loc.T("lists.error.name_taken")
		s.renderListRename(w, r, http.StatusUnprocessableEntity, data)
	case err != nil:
		s.serverError(w, r, err)
	default:
		slog.Info("list renamed", "id", id)
		http.Redirect(w, r, "/?renamed=1", http.StatusSeeOther)
	}
}

// handleListDeleteConfirm asks for confirmation; nothing changes on GET.
func (s *Server) handleListDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadList(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, "list_delete", "home", listDeleteData{List: l})
}

func (s *Server) handleListDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(r)
	if !ok {
		s.listNotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxListFormBytes)
	err := s.store.DeleteList(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.listNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	slog.Info("list deleted", "id", id)
	http.Redirect(w, r, "/?deleted=1", http.StatusSeeOther)
}
