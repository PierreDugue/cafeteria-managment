package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"unicode"
	"unicode/utf8"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/message"
	"cafeteria-management/internal/store"
	"cafeteria-management/internal/textnorm"
)

const (
	maxTemplateNameLen = 80
	maxTemplateBytes   = 1 << 20
)

type templatesData struct {
	Templates []store.Template
	Error     string
	Notice    string
}

type templateFormData struct {
	ID           int64 // 0 for a new template
	Name         string
	Subject      string
	Body         string
	Errors       []string
	Problems     []string
	Placeholders []placeholderView
	MaxSubject   int
	MaxBody      int
	MaxName      int
}

type templateDeleteData struct {
	Template store.Template
}

// validateTemplate checks a template as typed by the user and returns its
// cleaned name. It is shared by the template form and the compose page.
// Subject and body are checked like a message in AnyMode (SPEC §4.3).
func validateTemplate(loc i18n.Localizer, name, subject, body string) (cleanName string, errs, problems []string) {
	switch clean := textnorm.Clean(name); {
	case !utf8.ValidString(name) || hasControl(name):
		errs = append(errs, loc.T("templates.error.name_invalid"))
	case clean == "":
		errs = append(errs, loc.T("templates.error.name_required"))
	case utf8.RuneCountInString(clean) > maxTemplateNameLen:
		errs = append(errs, loc.T("templates.error.name_too_long", maxTemplateNameLen))
	default:
		cleanName = clean
	}
	problems = problemLines(loc, message.Check(subject, body, message.AnyMode))
	return cleanName, errs, problems
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// parseTemplateID reads the {id} of the route; it must be a positive integer.
func parseTemplateID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) renderTemplates(w http.ResponseWriter, r *http.Request, status int, errMsg, notice string) {
	list, err := s.store.Templates(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "templates", "templates", templatesData{Templates: list, Error: errMsg, Notice: notice})
}

func (s *Server) renderTemplateForm(w http.ResponseWriter, r *http.Request, status int, data templateFormData) {
	data.Placeholders = placeholderViews(s.localizer(r), message.AnyMode)
	data.MaxSubject, data.MaxBody, data.MaxName = message.MaxSubjectLen, message.MaxBodyLen, maxTemplateNameLen
	s.render(w, r, status, "template_form", "templates", data)
}

func (s *Server) templateNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderTemplates(w, r, http.StatusNotFound, s.localizer(r).T("templates.error.not_found"), "")
}

// handleTemplates lists the templates; ?saved=1 and ?deleted=1 show a notice.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	loc := s.localizer(r)
	notice := ""
	switch {
	case r.URL.Query().Get("saved") == "1":
		notice = loc.T("templates.saved")
	case r.URL.Query().Get("deleted") == "1":
		notice = loc.T("templates.deleted")
	}
	s.renderTemplates(w, r, http.StatusOK, "", notice)
}

func (s *Server) handleTemplateNew(w http.ResponseWriter, r *http.Request) {
	s.renderTemplateForm(w, r, http.StatusOK, templateFormData{})
}

// readTemplateForm reads and validates the template form of a POST. It
// returns ok=false when it has already answered (form too large).
func (s *Server) readTemplateForm(w http.ResponseWriter, r *http.Request, id int64) (data templateFormData, name string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateBytes)
	loc := s.localizer(r)
	data.ID = id
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			slog.Warn("templates: parse form", "err", err)
		}
		data.Errors = []string{loc.T("children.error.too_large")}
		s.renderTemplateForm(w, r, http.StatusUnprocessableEntity, data)
		return data, "", false
	}
	data.Name = r.PostForm.Get("name")
	data.Subject, data.Body = message.Normalize(r.PostForm.Get("subject"), r.PostForm.Get("body"))
	name, data.Errors, data.Problems = validateTemplate(loc, data.Name, data.Subject, data.Body)
	return data, name, true
}

func (s *Server) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	data, name, ok := s.readTemplateForm(w, r, 0)
	if !ok {
		return
	}
	if len(data.Errors) == 0 && len(data.Problems) == 0 {
		id, err := s.store.CreateTemplate(r.Context(), name, data.Subject, data.Body)
		switch {
		case err == nil:
			slog.Info("template created", "id", id)
			http.Redirect(w, r, "/templates?saved=1", http.StatusSeeOther)
			return
		case errors.Is(err, store.ErrNameTaken):
			data.Errors = append(data.Errors, s.localizer(r).T("templates.error.name_taken"))
		default:
			s.serverError(w, r, err)
			return
		}
	}
	s.renderTemplateForm(w, r, http.StatusUnprocessableEntity, data)
}

func (s *Server) handleTemplateEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTemplateID(r)
	if !ok {
		s.templateNotFound(w, r)
		return
	}
	t, err := s.store.Template(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.templateNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderTemplateForm(w, r, http.StatusOK, templateFormData{ID: t.ID, Name: t.Name, Subject: t.Subject, Body: t.Body})
}

func (s *Server) handleTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTemplateID(r)
	if !ok {
		s.templateNotFound(w, r)
		return
	}
	data, name, ok := s.readTemplateForm(w, r, id)
	if !ok {
		return
	}
	if len(data.Errors) == 0 && len(data.Problems) == 0 {
		err := s.store.UpdateTemplate(r.Context(), id, name, data.Subject, data.Body)
		switch {
		case err == nil:
			slog.Info("template updated", "id", id)
			http.Redirect(w, r, "/templates?saved=1", http.StatusSeeOther)
			return
		case errors.Is(err, store.ErrNotFound):
			s.templateNotFound(w, r)
			return
		case errors.Is(err, store.ErrNameTaken):
			data.Errors = append(data.Errors, s.localizer(r).T("templates.error.name_taken"))
		default:
			s.serverError(w, r, err)
			return
		}
	}
	s.renderTemplateForm(w, r, http.StatusUnprocessableEntity, data)
}

// handleTemplateDeleteConfirm asks for confirmation; nothing changes on GET.
func (s *Server) handleTemplateDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTemplateID(r)
	if !ok {
		s.templateNotFound(w, r)
		return
	}
	t, err := s.store.Template(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.templateNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "template_delete", "templates", templateDeleteData{Template: t})
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTemplateID(r)
	if !ok {
		s.templateNotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateBytes)
	err := s.store.DeleteTemplate(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.templateNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	slog.Info("template deleted", "id", id)
	http.Redirect(w, r, "/templates?deleted=1", http.StatusSeeOther)
}
