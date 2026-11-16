package server

import (
	"errors"
	"net/http"
	"strconv"

	"cafeteria-management/internal/store"
	"cafeteria-management/internal/textnorm"
)

// childRow is a child as shown in the selection list.
type childRow struct {
	store.Child
	Checked bool   // selected last time (only rows that can be emailed)
	Search  string // normalized text the browser filters on
}

type childrenData struct {
	List     store.List   // the list shown; zero if there is no list
	Lists    []store.List // all lists, for the selector (shown from 2 lists)
	Version  int64        // list version, posted back with the selection
	Error    string
	Rows     []childRow
	Families bool // rows are family entries (CSV export), not children
	NoEmail  int  // rows that cannot be selected: no valid email
}

// handleChildren shows the entries of one list to select: ?list=<id>, or
// the list used last. Opening a list changes nothing (no state change on
// GET): only an import or "Continuer" make a list the last used.
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var listID int64
	if q.Has("list") {
		// A bad value is an unknown list: -1 matches no row.
		listID = -1
		if id, err := strconv.ParseInt(q.Get("list"), 10, 64); err == nil && id > 0 {
			listID = id
		}
	}
	s.renderChildren(w, r, http.StatusOK, listID, "")
}

// renderChildren shows the selection page. listID 0 means the default list;
// a list that does not exist falls back to the default list, with a 404 and
// an error unless the caller already has an error to show.
func (s *Server) renderChildren(w http.ResponseWriter, r *http.Request, status int, listID int64, errMsg string) {
	ctx := r.Context()
	var list store.List
	var err error
	if listID != 0 {
		list, err = s.store.List(ctx, listID)
	}
	if listID == 0 || errors.Is(err, store.ErrNotFound) {
		if listID != 0 && errMsg == "" {
			status, errMsg = http.StatusNotFound, s.localizer(r).T("lists.error.not_found")
		}
		list, err = s.store.DefaultList(ctx)
	}
	noList := errors.Is(err, store.ErrNotFound)
	if err != nil && !noList {
		s.serverError(w, r, err)
		return
	}
	data := childrenData{Error: errMsg}
	if !noList {
		// Version first: an import in between can only make it look stale.
		if data.Version, err = s.store.ListVersion(ctx, list.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		data.List = list
	}
	if data.Lists, err = s.store.Lists(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if noList {
		s.render(w, r, status, "children", "children", data)
		return
	}
	children, err := s.store.Children(ctx, list.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	saved, err := s.store.SavedSelection(ctx, list.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Rows = make([]childRow, len(children))
	for i, c := range children {
		data.Rows[i] = childRow{
			Child:   c,
			Checked: c.CanEmail() && saved[c.ID],
			Search:  textnorm.Key(c.Name + " " + c.FamilyNumber + " " + c.ParentName + " " + c.Email + " " + c.InvalidEmail),
		}
		if c.FamilyNumber != "" {
			data.Families = true
		}
		if !c.CanEmail() {
			data.NoEmail++
		}
	}
	s.render(w, r, status, "children", "children", data)
}
