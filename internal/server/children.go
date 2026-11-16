package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"cafeteria-management/internal/recipients"
	"cafeteria-management/internal/store"
	"cafeteria-management/internal/textnorm"
)

const (
	maxComposeBytes = 1 << 20
	// maxSelectedChildren bounds the distinct IDs of one request, far above
	// a school's size and below SQLite's bound-variable limit.
	maxSelectedChildren = 5000
)

// childRow is a child as shown in the selection list.
type childRow struct {
	store.Child
	Search string // normalized text the browser filters on
}

type childrenData struct {
	Error    string
	Rows     []childRow
	Families bool // rows are family entries (CSV export), not children
	NoEmail  int  // rows that cannot be selected: no valid email
}

type composeData struct {
	Recipients []recipients.Recipient
	Dropped    int  // selected children that no longer exist
	Families   bool // family entries (CSV export), not children
}

// handleChildren shows the list of children to select.
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	s.renderChildren(w, r, http.StatusOK, "")
}

func (s *Server) renderChildren(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	children, err := s.store.Children(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := childrenData{Error: errMsg, Rows: make([]childRow, len(children))}
	for i, c := range children {
		data.Rows[i] = childRow{
			Child:  c,
			Search: textnorm.Key(c.Name + " " + c.FamilyNumber + " " + c.ParentName + " " + c.Email + " " + c.InvalidEmail),
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

// handleCompose shows the recipients (one per parent) of the selected
// children. The message form will be added here (SPEC §4.3).
func (s *Server) handleCompose(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxComposeBytes)
	loc := s.localizer(r)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.too_large"))
			return
		}
		slog.Warn("compose: parse form", "err", err)
		s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.none_selected"))
		return
	}

	ids := parseIDs(r.PostForm["child"])
	if len(ids) > maxSelectedChildren {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.too_many", maxSelectedChildren))
		return
	}
	children, err := s.store.ChildrenByIDs(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(children) == 0 {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.none_selected"))
		return
	}

	families := false
	for _, c := range children {
		// Only a tampered form can post such an ID: refuse the whole request.
		if !c.CanEmail() {
			slog.Warn("compose: entry without a valid email selected", "id", c.ID)
			s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.no_email"))
			return
		}
		if c.FamilyNumber != "" {
			families = true
		}
	}

	s.render(w, r, http.StatusOK, "compose", "children", composeData{
		Recipients: recipients.Group(children),
		Dropped:    len(ids) - len(children),
		Families:   families,
	})
}

// parseIDs keeps the positive integers of values, without duplicates.
func parseIDs(values []string) []int64 {
	seen := map[int64]bool{}
	ids := []int64{}
	for _, v := range values {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}
