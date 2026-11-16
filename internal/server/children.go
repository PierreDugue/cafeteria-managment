package server

import (
	"net/http"

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
	Version  int64 // data version, posted back with the selection
	Error    string
	Rows     []childRow
	Families bool // rows are family entries (CSV export), not children
	NoEmail  int  // rows that cannot be selected: no valid email
}

// handleChildren shows the list of children to select.
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	s.renderChildren(w, r, http.StatusOK, "")
}

func (s *Server) renderChildren(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	// Version first: an import in between can only make it look stale.
	version, err := s.store.DataVersion(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	children, err := s.store.Children(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	saved, err := s.store.SavedSelection(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := childrenData{Version: version, Error: errMsg, Rows: make([]childRow, len(children))}
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
