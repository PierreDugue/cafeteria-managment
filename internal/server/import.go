package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"cafeteria-management/internal/importer"
	"cafeteria-management/internal/store"
)

const (
	maxUploadMB    = 20
	maxUploadBytes = maxUploadMB << 20
	historyLimit   = 20
	// maxShownIssues is the most errors, and the most warnings, the preview
	// lists: a crafted or broken file can have thousands.
	maxShownIssues = 100
)

type importFormData struct {
	Error   string
	Done    *store.ImportRecord
	History []store.ImportRecord
}

type importPreviewData struct {
	FileName  string
	Result    *importer.Result
	Summary   store.ImportSummary
	NoChanges bool
	Token     string // empty when the file has errors and cannot be imported
	NoEmail   int    // rows without a valid email (family layout)

	// First maxShownIssues errors and warnings, and how many are not shown.
	Errors       []importer.Issue
	MoreErrors   int
	Warnings     []importer.Issue
	MoreWarnings int
}

// firstIssues returns at most maxShownIssues issues and the number left out.
func firstIssues(is []importer.Issue) ([]importer.Issue, int) {
	if len(is) <= maxShownIssues {
		return is, 0
	}
	return is[:maxShownIssues], len(is) - maxShownIssues
}

// handleImportForm shows the upload form and the import history.
// After a successful import, ?done=<id> shows a confirmation.
func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	var doneID int64
	if v := r.URL.Query().Get("done"); v != "" {
		doneID, _ = strconv.ParseInt(v, 10, 64)
	}
	s.renderImportForm(w, r, http.StatusOK, "", doneID)
}

func (s *Server) renderImportForm(w http.ResponseWriter, r *http.Request, status int, errMsg string, doneID int64) {
	history, err := s.store.Imports(r.Context(), historyLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := importFormData{Error: errMsg, History: history}
	for i := range history {
		if doneID != 0 && history[i].ID == doneID {
			data.Done = &history[i]
		}
	}
	s.render(w, r, status, "import", "import", data)
}

// handleImportPreview reads the uploaded file and shows what importing it
// would change. Nothing is saved: the checked rows wait for confirmation.
func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	loc := s.localizer(r)
	userError := func(msg string) {
		s.renderImportForm(w, r, http.StatusUnprocessableEntity, msg, 0)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			userError(loc.T("import.error.too_large", maxUploadMB))
		case errors.Is(err, http.ErrMissingFile):
			userError(loc.T("import.error.no_file"))
		default:
			userError(loc.T("import.error.unreadable"))
		}
		return
	}
	defer file.Close()

	fileName := filepath.Base(header.Filename)
	var res *importer.Result
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".xlsx":
		res, err = importer.ParseXLSX(file)
	case ".csv":
		res, err = importer.ParseCSV(file)
	default:
		userError(loc.T("import.error.format"))
		return
	}
	if err != nil {
		var missing *importer.MissingColumnsError
		switch {
		case errors.As(err, &missing):
			names := make([]string, len(missing.Columns))
			for i, c := range missing.Columns {
				names[i] = loc.T("import.quoted", loc.T("import.column."+string(c)))
			}
			userError(loc.T("import.error.missing_columns", strings.Join(names, ", ")))
		case errors.Is(err, importer.ErrTooLarge):
			userError(loc.T("import.error.too_large", maxUploadMB))
		case errors.Is(err, importer.ErrTooManyColumns):
			userError(loc.T("import.error.too_many_columns"))
		case errors.Is(err, importer.ErrEmpty):
			userError(loc.T("import.error.empty"))
		default:
			slog.Warn("unreadable import file", "err", err)
			userError(loc.T("import.error.unreadable"))
		}
		return
	}

	data := importPreviewData{FileName: fileName, Result: res}
	data.Errors, data.MoreErrors = firstIssues(res.Errors)
	data.Warnings, data.MoreWarnings = firstIssues(res.Warnings)
	for _, row := range res.Rows {
		if row.Email == "" {
			data.NoEmail++
		}
	}
	if len(res.Errors) == 0 {
		rows := toImportRows(res.Rows)
		sum, err := s.store.ImportFamilies(r.Context(), store.ImportRequest{
			FileName: fileName, RowsRead: res.RowsRead, Rows: rows, DryRun: true,
		})
		if err != nil {
			s.serverError(w, r, fmt.Errorf("preview import: %w", err))
			return
		}
		data.Summary = sum
		data.NoChanges = !hasChanges(sum.Parents) && !hasChanges(sum.Children)
		data.Token = s.pending.put(pendingImport{fileName: fileName, rowsRead: res.RowsRead, rows: rows, base: sum.Base})
	}
	s.render(w, r, http.StatusOK, "import_preview", "import", data)
}

// handleImportConfirm saves a previously checked file, only if the data has
// not changed since its preview. The token stays valid if saving fails.
func (s *Server) handleImportConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.PostFormValue("token")
	item, ok := s.pending.get(token)
	if !ok {
		s.renderImportForm(w, r, http.StatusUnprocessableEntity, s.localizer(r).T("import.error.expired"), 0)
		return
	}

	sum, err := s.store.ImportFamilies(r.Context(), store.ImportRequest{
		FileName: item.fileName, RowsRead: item.rowsRead, Rows: item.rows, Base: &item.base,
	})
	if errors.Is(err, store.ErrStaleImport) {
		s.pending.remove(token)
		s.renderImportForm(w, r, http.StatusConflict, s.localizer(r).T("import.error.stale"), 0)
		return
	}
	if err != nil {
		s.serverError(w, r, fmt.Errorf("import: %w", err))
		return
	}
	s.pending.remove(token)
	// Counts only: no names or emails in logs.
	slog.Info("import saved", "id", sum.ImportID,
		"children_added", sum.Children.Added, "children_updated", sum.Children.Updated, "children_deleted", sum.Children.Deleted,
		"parents_added", sum.Parents.Added, "parents_updated", sum.Parents.Updated, "parents_deleted", sum.Parents.Deleted)

	http.Redirect(w, r, "/import?done="+strconv.FormatInt(sum.ImportID, 10), http.StatusSeeOther)
}

func toImportRows(rows []importer.Row) []store.ImportRow {
	out := make([]store.ImportRow, len(rows))
	for i, r := range rows {
		out[i] = store.ImportRow{
			ParentName: r.ParentName, ChildName: r.ChildName, Email: r.Email,
			FamilyNumber: r.FamilyNumber, InvalidEmail: r.InvalidEmail,
		}
	}
	return out
}

func hasChanges(c store.Changes) bool {
	return c.Added+c.Updated+c.Deleted > 0
}
