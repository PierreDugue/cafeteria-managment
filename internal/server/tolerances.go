package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"cafeteria-management/internal/store"
)

const maxToleranceBytes = 1 << 20

// handleTolerances replaces the "Tolérance" flags of a list with the
// posted family numbers (SPEC §4.2). With the htmx header (the page's
// script) it answers bare status codes; without it, the full pages. It
// never changes the selection or the list version.
func (s *Server) handleTolerances(w http.ResponseWriter, r *http.Request) {
	loc := s.localizer(r)
	ajax := r.Header.Get("HX-Request") == "true"
	// fail answers an error: a status and the message for the script, the
	// selection page for a plain form.
	fail := func(status int, listID int64, msg string) {
		if ajax {
			http.Error(w, msg, status)
			return
		}
		s.renderChildren(w, r, status, listID, msg)
	}

	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || listID <= 0 {
		listID = -1 // matches no list: 404
	}
	if _, err := s.store.List(r.Context(), listID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if ajax {
				http.Error(w, loc.T("lists.error.not_found"), http.StatusNotFound)
				return
			}
			s.renderChildren(w, r, http.StatusNotFound, listID, "")
			return
		}
		s.serverError(w, r, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxToleranceBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(http.StatusUnprocessableEntity, listID, loc.T("children.error.too_large"))
			return
		}
		slog.Warn("tolerances: parse form", "err", err)
		fail(http.StatusUnprocessableEntity, listID, loc.T("children.error.tolerance_invalid"))
		return
	}
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 0 {
		fail(http.StatusUnprocessableEntity, listID, loc.T("children.error.tolerance_invalid"))
		return
	}
	var families []string
	for _, f := range r.PostForm["family"] {
		if f = strings.TrimSpace(f); f != "" {
			families = append(families, f)
		}
	}

	switch err := s.store.SetTolerances(r.Context(), listID, version, families); {
	case errors.Is(err, store.ErrStaleSelection):
		fail(http.StatusConflict, listID, loc.T("compose.error.stale"))
		return
	case errors.Is(err, store.ErrUnknownFamily):
		fail(http.StatusUnprocessableEntity, listID, loc.T("children.error.tolerance_invalid"))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	// Counts only: family numbers are personal data.
	slog.Info("tolerances saved", "list_id", listID, "flags", len(families))

	if ajax {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/children?list="+strconv.FormatInt(listID, 10)+"&tolerance=1", http.StatusSeeOther)
}
