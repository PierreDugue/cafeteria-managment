package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/message"
	"cafeteria-management/internal/recipients"
	"cafeteria-management/internal/store"
)

const (
	maxComposeBytes = 1 << 20
	// maxSelectedChildren bounds the distinct IDs of one request, far above
	// a school's size and below SQLite's bound-variable limit.
	maxSelectedChildren = 5000
)

// recipientRow is a recipient as shown on the compose page.
type recipientRow struct {
	ParentID   int64
	ParentName string
	Email      string
	Children   string // child names (children data) or "number name" entries (family data)
}

type composeData struct {
	Version      int64
	IDs          []int64
	Families     bool // family entries (CSV export), not children
	Recipients   []recipientRow
	Subject      string
	Body         string
	Templates    []store.Template
	Placeholders []placeholderView
	PreviewTo    int64 // parent ID chosen in the preview picker
	Preview      *message.Email
	Problems     []string // localized lines, shown instead of the preview
	TemplateName string   // "save as template" input
	Errors       []string
	Notice       string
	MaxSubject   int
	MaxBody      int
	MaxName      int
}

// handleCompose shows the message form for the selected children and
// handles its buttons (load a template, preview, save as template). The
// selection travels in the form with the data version (SPEC §4.2, §4.3).
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
	if len(ids) == 0 {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.none_selected"))
		return
	}

	// A missing or invalid version is as stale as an old one.
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 0 {
		s.renderChildren(w, r, http.StatusConflict, loc.T("compose.error.stale"))
		return
	}
	children, err := s.store.SelectedChildren(r.Context(), version, ids)
	if errors.Is(err, store.ErrStaleSelection) {
		s.renderChildren(w, r, http.StatusConflict, loc.T("compose.error.stale"))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(children) == 0 {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.none_selected"))
		return
	}
	for _, c := range children {
		// Only a tampered form can post such an ID: refuse the whole request.
		if !c.CanEmail() {
			slog.Warn("compose: entry without a valid email selected", "id", c.ID)
			s.renderChildren(w, r, http.StatusUnprocessableEntity, loc.T("children.error.no_email"))
			return
		}
	}

	// Remembered for the next visit of the list. A failure must not block
	// the user: the selection is only a convenience.
	if err := s.store.SaveSelection(r.Context(), entryIDs(children)); err != nil {
		slog.Warn("compose: save selection", "err", err)
	}

	group := recipients.Group(children)
	mode := message.ModeOf(group)
	subject, body := message.Normalize(r.PostForm.Get("subject"), r.PostForm.Get("body"))

	data := composeData{
		Version:      version,
		Families:     mode == message.FamilyMode,
		Recipients:   recipientRows(group, mode),
		Placeholders: placeholderViews(loc, mode),
		MaxSubject:   message.MaxSubjectLen,
		MaxBody:      message.MaxBodyLen,
		MaxName:      maxTemplateNameLen,
	}
	data.IDs = entryIDs(children)
	if id, err := strconv.ParseInt(r.PostForm.Get("preview_to"), 10, 64); err == nil {
		data.PreviewTo = id
	}
	status := http.StatusOK

	action := r.PostForm.Get("action")
	switch action {
	case "load":
		if id, err := strconv.ParseInt(r.PostForm.Get("template"), 10, 64); err == nil && id > 0 {
			t, err := s.store.Template(r.Context(), id)
			switch {
			case errors.Is(err, store.ErrNotFound):
				data.Errors = append(data.Errors, loc.T("templates.error.not_found"))
			case err != nil:
				s.serverError(w, r, err)
				return
			default:
				subject, body = t.Subject, t.Body
				data.Notice = loc.T("compose.loaded", t.Name)
			}
		}
	case "preview":
		if problems := message.Check(subject, body, mode); problems != nil {
			data.Problems = problemLines(loc, problems)
			break
		}
		rec := group[0]
		for _, g := range group {
			if g.ParentID == data.PreviewTo {
				rec = g
			}
		}
		data.PreviewTo = rec.ParentID
		email, err := message.Render(subject, body, rec, mode, loc.T("message.join_and"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Preview = &email
	case "save_template":
		data.TemplateName = r.PostForm.Get("template_name")
		name, errs, problems := validateTemplate(loc, data.TemplateName, subject, body)
		if len(errs) == 0 && len(problems) == 0 {
			_, err := s.store.CreateTemplate(r.Context(), name, subject, body)
			switch {
			case errors.Is(err, store.ErrNameTaken):
				errs = append(errs, loc.T("templates.error.name_taken"))
			case err != nil:
				s.serverError(w, r, err)
				return
			default:
				data.Notice = loc.T("compose.saved", name)
				data.TemplateName = ""
			}
		}
		if len(errs) > 0 || len(problems) > 0 {
			data.Errors, data.Problems = errs, problems
			status = http.StatusUnprocessableEntity
		}
	}
	slog.Debug("compose", "action", action, "children", len(children), "recipients", len(group))

	data.Subject, data.Body = subject, body
	data.Templates, err = s.store.Templates(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "compose", "children", data)
}

func recipientRows(group []recipients.Recipient, mode message.Mode) []recipientRow {
	rows := make([]recipientRow, len(group))
	for i, g := range group {
		row := recipientRow{ParentID: g.ParentID, ParentName: g.ParentName, Email: g.Email}
		if mode == message.FamilyMode {
			entries := make([]string, len(g.Children))
			for j, name := range g.Children {
				entries[j] = g.FamilyNumbers[j] + " " + name
			}
			row.Children = strings.Join(entries, ", ")
		} else {
			row.Children = strings.Join(g.Children, ", ")
		}
		rows[i] = row
	}
	return rows
}

// placeholderView is a placeholder with its description for the page.
type placeholderView struct {
	Name string
	Desc string
	Note string // where it is available, only on the template form
}

// placeholderViews lists the placeholders of m with their description in
// the language of loc. AnyMode (template form) adds where each one works.
func placeholderViews(loc i18n.Localizer, m message.Mode) []placeholderView {
	var out []placeholderView
	for _, name := range message.Placeholders(m) {
		v := placeholderView{Name: name, Desc: loc.T("placeholder." + name)}
		switch {
		case name == message.ParentName && m == message.FamilyMode:
			v.Desc = loc.T("placeholder.parent_name_families")
		case name == message.ParentName && m == message.AnyMode:
			v.Desc = loc.T("placeholder.parent_name_any")
		}
		if m == message.AnyMode {
			if name == message.FamilyNumber {
				v.Note = loc.T("placeholder.only_families")
			}
		}
		out = append(out, v)
	}
	return out
}

// problemLines turns message problems into localized lines.
func problemLines(loc i18n.Localizer, ps []message.Problem) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		label, limit := loc.T("compose.body"), message.MaxBodyLen
		if p.Field == message.FieldSubject {
			label, limit = loc.T("compose.subject"), message.MaxSubjectLen
		}
		var text string
		switch p.Kind {
		case message.KindTooLong:
			text = loc.T("problem.too_long", limit)
		case message.KindEmpty, message.KindInvalidChar:
			text = loc.T("problem." + string(p.Kind))
		default:
			text = loc.T("problem."+string(p.Kind), p.Token)
		}
		out[i] = loc.T("problem.line", label, text)
	}
	return out
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

func entryIDs(children []store.Child) []int64 {
	ids := make([]int64, len(children))
	for i, c := range children {
		ids[i] = c.ID
	}
	return ids
}
