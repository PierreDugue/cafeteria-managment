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
	ListID       int64
	ListName     string // shown only
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

// selection is a checked selection of entries of one list, as posted by the
// selection page, the compose page and the confirmation page.
type selection struct {
	listID   int64
	version  int64
	list     store.List
	children []store.Child
	group    []recipients.Recipient
	mode     message.Mode
}

// readSelection reads and checks the selection of a POST (body limit, list,
// IDs, version, stale check, entries without email). It returns ok=false
// when it has already answered. Compose and send share it, so both refuse
// the same inputs the same way (SPEC §4.0, §4.2, §4.4).
func (s *Server) readSelection(w http.ResponseWriter, r *http.Request) (selection, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxComposeBytes)
	loc := s.localizer(r)
	// listID stays 0 (the default list) until the form is read and valid:
	// error pages then show the posted list, or else the default one.
	var sel selection
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderChildren(w, r, http.StatusUnprocessableEntity, 0, loc.T("children.error.too_large"))
			return sel, false
		}
		slog.Warn("parse form", "err", err)
		s.renderChildren(w, r, http.StatusUnprocessableEntity, 0, loc.T("children.error.none_selected"))
		return sel, false
	}

	listID := int64(0)
	if id, err := strconv.ParseInt(r.PostForm.Get("list"), 10, 64); err == nil && id > 0 {
		listID = id
	}
	ids := parseIDs(r.PostForm["child"])
	if len(ids) > maxSelectedChildren {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, listID, loc.T("children.error.too_many", maxSelectedChildren))
		return sel, false
	}
	if len(ids) == 0 {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, listID, loc.T("children.error.none_selected"))
		return sel, false
	}

	// A missing or invalid list or version is as stale as an old one.
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if listID == 0 || err != nil || version < 0 {
		s.renderChildren(w, r, http.StatusConflict, listID, loc.T("compose.error.stale"))
		return sel, false
	}
	children, err := s.store.SelectedChildren(r.Context(), listID, version, ids)
	if errors.Is(err, store.ErrStaleSelection) {
		s.renderChildren(w, r, http.StatusConflict, listID, loc.T("compose.error.stale"))
		return sel, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return sel, false
	}
	if len(children) == 0 {
		s.renderChildren(w, r, http.StatusUnprocessableEntity, listID, loc.T("children.error.none_selected"))
		return sel, false
	}
	for _, c := range children {
		// Only a tampered form can post such an ID: refuse the whole request.
		if !c.CanEmail() {
			slog.Warn("entry without a valid email selected", "id", c.ID)
			s.renderChildren(w, r, http.StatusUnprocessableEntity, listID, loc.T("children.error.no_email"))
			return sel, false
		}
	}

	list, err := s.store.List(r.Context(), listID)
	if errors.Is(err, store.ErrNotFound) { // deleted just now
		s.renderChildren(w, r, http.StatusConflict, listID, loc.T("compose.error.stale"))
		return sel, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return sel, false
	}

	group := recipients.Group(children)
	return selection{
		listID: listID, version: version, list: list, children: children,
		group: group, mode: message.ModeOf(group),
	}, true
}

// composeBase builds the compose page data for a selection.
func composeBase(loc i18n.Localizer, sel selection) composeData {
	return composeData{
		ListID:       sel.listID,
		ListName:     sel.list.Name,
		Version:      sel.version,
		IDs:          entryIDs(sel.children),
		Families:     sel.mode == message.FamilyMode,
		Recipients:   recipientRows(sel.group, sel.mode),
		Placeholders: placeholderViews(loc, sel.mode),
		MaxSubject:   message.MaxSubjectLen,
		MaxBody:      message.MaxBodyLen,
		MaxName:      maxNameLen,
	}
}

// renderCompose adds the templates list to data and shows the compose page.
func (s *Server) renderCompose(w http.ResponseWriter, r *http.Request, status int, data composeData) {
	var err error
	if data.Templates, err = s.store.Templates(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "compose", "children", data)
}

// handleCompose shows the message form for the selected children and
// handles its buttons (load a template, preview, save as template, send).
// The selection travels in the form with its list and the list version
// (SPEC §4.0, §4.2, §4.3).
func (s *Server) handleCompose(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.readSelection(w, r)
	if !ok {
		return
	}
	loc := s.localizer(r)

	// Remembered for the next visit of the list. A failure must not block
	// the user: the selection is only a convenience.
	if err := s.store.SaveSelection(r.Context(), sel.listID, entryIDs(sel.children)); err != nil {
		slog.Warn("compose: save selection", "err", err)
	}

	group, mode := sel.group, sel.mode
	subject, body := message.Normalize(r.PostForm.Get("subject"), r.PostForm.Get("body"))
	data := composeBase(loc, sel)
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
		rec := group[0]
		for _, g := range group {
			if g.ParentID == data.PreviewTo {
				rec = g
			}
		}
		data.PreviewTo = rec.ParentID
		emails, problems, err := message.RenderAll(subject, body, []recipients.Recipient{rec}, mode, loc.T("message.join_and"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if problems != nil {
			data.Problems = problemLines(loc, problems)
			break
		}
		data.Preview = &emails[0]
	case "send":
		emails, problems, err := message.RenderAll(subject, body, group, mode, loc.T("message.join_and"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if problems != nil {
			data.Problems = problemLines(loc, problems)
			status = http.StatusUnprocessableEntity
			break
		}
		s.renderSendConfirm(w, r, sel, subject, body, emails)
		return
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
	slog.Debug("compose", "action", action, "children", len(sel.children), "recipients", len(group))

	data.Subject, data.Body = subject, body
	s.renderCompose(w, r, status, data)
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
		case message.KindRenderedTooLong:
			text = loc.T("problem.rendered_too_long", limit, p.Count, p.Recipient)
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
