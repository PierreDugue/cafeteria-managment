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
	"cafeteria-management/internal/textnorm"
)

const (
	maxUploadMB    = 20
	maxUploadBytes = maxUploadMB << 20
	historyLimit   = 20
	// maxShownIssues is the most errors, and the most warnings, the preview
	// lists: a crafted or broken file can have thousands.
	maxShownIssues = 100
	// maxConfirmBytes bounds the confirmation form (a token and a checkbox).
	maxConfirmBytes = 1 << 20
	// newListChoice is the value of the "new list" option of the target select.
	newListChoice = "new"
)

type importFormData struct {
	Error   string
	Done    *store.ImportRecord
	History []store.ImportRecord
	Lists   []store.List
	// Target and NewName are the typed values of the target fields, shown
	// again on errors. Target is "", "new" or a list ID as typed.
	Target  string
	NewName string
}

// importTarget holds the target fields as typed by the user.
type importTarget struct {
	Choice  string
	NewName string
}

type importPreviewData struct {
	FileName  string
	Result    *importer.Result
	Summary   store.ImportSummary
	NoChanges bool
	Token     string // empty when the file has errors and cannot be imported
	NoEmail   int    // rows without a valid email (family layout)
	NewList   bool   // the import creates ListName; else it replaces it
	ListName  string // target list (cleaned name of a new list)
	Error     string // set only when a replacement is not confirmed

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
	s.renderImportForm(w, r, http.StatusOK, "", doneID, importTarget{})
}

func (s *Server) renderImportForm(w http.ResponseWriter, r *http.Request, status int, errMsg string, doneID int64, typed importTarget) {
	history, err := s.store.Imports(r.Context(), historyLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lists, err := s.store.Lists(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := importFormData{Error: errMsg, History: history, Lists: lists, Target: typed.Choice, NewName: typed.NewName}
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

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	// Parse the whole form first: the target is checked before the file is
	// read (SPEC §4.0), and its errors show whatever the file is.
	err := r.ParseMultipartForm(32 << 20)
	typed := importTarget{Choice: r.FormValue("list"), NewName: r.FormValue("list_name")}
	userError := func(msg string) {
		s.renderImportForm(w, r, http.StatusUnprocessableEntity, msg, 0, typed)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			userError(loc.T("import.error.too_large", maxUploadMB))
		} else {
			userError(loc.T("import.error.unreadable"))
		}
		return
	}

	target, errMsg, err := s.checkImportTarget(r, typed)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errMsg != "" {
		userError(errMsg)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			userError(loc.T("import.error.no_file"))
		} else {
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

	data := importPreviewData{FileName: fileName, Result: res, NewList: target.listID == 0, ListName: target.name}
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
			ListID: target.listID, NewListName: target.newName,
		})
		switch {
		case errors.Is(err, store.ErrNotFound):
			userError(loc.T("lists.error.not_found"))
			return
		case errors.Is(err, store.ErrNameTaken):
			userError(loc.T("lists.error.name_taken"))
			return
		case err != nil:
			s.serverError(w, r, fmt.Errorf("preview import: %w", err))
			return
		}
		data.Summary = sum
		data.ListName = sum.ListName
		data.NoChanges = !hasChanges(sum.Parents) && !hasChanges(sum.Children)
		data.Token = s.pending.put(pendingImport{
			fileName: fileName, rowsRead: res.RowsRead, rows: rows,
			listID: target.listID, newListName: target.newName, base: sum.Base,
			preview: withoutToken(data),
		})
	}
	s.render(w, r, http.StatusOK, "import_preview", "import", data)
}

// checkedTarget is a validated import target: an existing list (listID) or
// a new one (newName), and the list name shown to the user.
type checkedTarget struct {
	listID  int64
	newName string
	name    string
}

// checkImportTarget validates the target fields of the import form. errMsg
// is a localized user error; err is an unexpected failure.
func (s *Server) checkImportTarget(r *http.Request, typed importTarget) (t checkedTarget, errMsg string, err error) {
	loc := s.localizer(r)
	lists, err := s.store.Lists(r.Context())
	if err != nil {
		return t, "", err
	}
	switch typed.Choice {
	case "":
		return t, loc.T("import.error.list_required"), nil
	case newListChoice:
		clean, msg := validateListName(loc, typed.NewName)
		if msg != "" {
			return t, msg, nil
		}
		key := textnorm.Key(clean)
		for _, l := range lists {
			if textnorm.Key(l.Name) == key {
				return t, loc.T("lists.error.name_taken"), nil
			}
		}
		return checkedTarget{newName: clean, name: clean}, "", nil
	}
	id, perr := strconv.ParseInt(typed.Choice, 10, 64)
	if perr == nil && id > 0 {
		for _, l := range lists {
			if l.ID == id {
				return checkedTarget{listID: id, name: l.Name}, "", nil
			}
		}
	}
	return t, loc.T("lists.error.not_found"), nil
}

// withoutToken returns the preview as stored with the pending import.
func withoutToken(d importPreviewData) importPreviewData {
	d.Token, d.Error = "", ""
	return d
}

// handleImportConfirm saves a previously checked file into the list chosen
// at preview time, only if that list has not changed since its preview. A
// replacement also needs the confirmation checkbox, checked here whatever
// the browser does (SPEC §4.0). The token stays valid if saving fails.
func (s *Server) handleImportConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxConfirmBytes)
	loc := s.localizer(r)
	token := r.PostFormValue("token")
	item, ok := s.pending.get(token)
	if !ok {
		s.renderImportForm(w, r, http.StatusUnprocessableEntity, loc.T("import.error.expired"), 0, importTarget{})
		return
	}

	// Nothing is read from or written to the database before this check.
	if item.listID != 0 && r.PostFormValue("confirm_replace") != "1" {
		data := item.preview
		data.Token = token
		data.Error = loc.T("import.error.replace_unconfirmed", data.ListName)
		s.render(w, r, http.StatusUnprocessableEntity, "import_preview", "import", data)
		return
	}

	req := store.ImportRequest{
		FileName: item.fileName, RowsRead: item.rowsRead, Rows: item.rows,
		ListID: item.listID, NewListName: item.newListName,
	}
	if item.listID != 0 {
		req.Base = &item.base
	}
	sum, err := s.store.ImportFamilies(r.Context(), req)
	var conflict string
	switch {
	case errors.Is(err, store.ErrStaleImport):
		conflict = "import.error.stale"
	case errors.Is(err, store.ErrNotFound):
		conflict = "import.error.list_gone"
	case errors.Is(err, store.ErrNameTaken):
		conflict = "import.error.list_name_taken"
	}
	if conflict != "" {
		s.pending.remove(token)
		s.renderImportForm(w, r, http.StatusConflict, loc.T(conflict), 0, importTarget{})
		return
	}
	if err != nil {
		s.serverError(w, r, fmt.Errorf("import: %w", err))
		return
	}
	s.pending.remove(token)
	// IDs and counts only: no list names, names or emails in logs.
	slog.Info("import saved", "id", sum.ImportID, "list_id", sum.ListID,
		"children_added", sum.Children.Added, "children_updated", sum.Children.Updated, "children_deleted", sum.Children.Deleted,
		"parents_added", sum.Parents.Added, "parents_updated", sum.Parents.Updated, "parents_deleted", sum.Parents.Deleted,
		"tolerances_removed", sum.TolerancesRemoved)

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
