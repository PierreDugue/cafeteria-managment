// Package importer reads the families Excel file and validates its rows.
// It does not touch the database: see store.ImportFamilies for that.
package importer

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/xuri/excelize/v2"

	"cafeteria-management/internal/textnorm"
)

// maxUnzipBytes is far above any real families file.
const maxUnzipBytes = 200 << 20

// Column is one of the columns the file must contain.
type Column string

// Required columns.
const (
	ColParentName Column = "parent_name"
	ColChildName  Column = "child_name"
	ColEmail      Column = "email"
)

var columns = []Column{ColParentName, ColChildName, ColEmail}

// headerAliases maps accepted header names (as textnorm keys) to columns.
var headerAliases = map[string]Column{
	"nom parent":       ColParentName,
	"nom du parent":    ColParentName,
	"nom parents":      ColParentName,
	"nom des parents":  ColParentName,
	"parent":           ColParentName,
	"parents":          ColParentName,
	"parent name":      ColParentName,
	"nom enfant":       ColChildName,
	"nom de l'enfant":  ColChildName,
	"nom de l’enfant":  ColChildName,
	"enfant":           ColChildName,
	"child":            ColChildName,
	"child name":       ColChildName,
	"adresse email":    ColEmail,
	"adresse e-mail":   ColEmail,
	"adresse mail":     ColEmail,
	"adresse courriel": ColEmail,
	"email":            ColEmail,
	"e-mail":           ColEmail,
	"mail":             ColEmail,
	"courriel":         ColEmail,
}

// Errors that make the whole file unusable.
var (
	ErrUnreadable = errors.New("file is not a readable .xlsx file")
	ErrEmpty      = errors.New("file contains no data")
)

// MissingColumnsError reports required columns not found in the header row.
type MissingColumnsError struct {
	Columns []Column
}

func (e *MissingColumnsError) Error() string {
	return fmt.Sprintf("missing columns: %v", e.Columns)
}

// IssueCode identifies a problem found on a row.
type IssueCode string

// Blocking issues: the row is invalid.
const (
	IssueMissingParentName IssueCode = "missing_parent_name"
	IssueMissingChildName  IssueCode = "missing_child_name"
	IssueMissingEmail      IssueCode = "missing_email"
	IssueInvalidEmail      IssueCode = "invalid_email"
)

// Warnings: the file is usable, but the user should know.
const (
	// IssueDuplicateRow: same child and email as an earlier row; ignored.
	IssueDuplicateRow IssueCode = "duplicate_row"
	// IssueParentNameConflict: same email as an earlier row but another
	// parent name; the first name is kept.
	IssueParentNameConflict IssueCode = "parent_name_conflict"
)

// Issue is a problem found on one line of the sheet.
type Issue struct {
	Line  int // 1-based line number, as shown in Excel
	Code  IssueCode
	Value string // offending value, if any
}

// Row is a valid line of the file. Values are cleaned; Email is lower-case.
type Row struct {
	Line       int
	ParentName string
	ChildName  string
	Email      string
}

// Result is the outcome of parsing a file.
type Result struct {
	Sheet    string
	RowsRead int     // non-empty data lines
	Rows     []Row   // valid, de-duplicated rows
	Errors   []Issue // invalid lines; the file must be fixed before import
	Warnings []Issue
}

// Parse reads the first sheet of an .xlsx file. The first non-empty line is
// the header; other columns than the required ones are ignored.
func Parse(r io.Reader) (*Result, error) {
	// Limit unzipped size: a small crafted file could otherwise expand to
	// gigabytes (excelize's default limit is 16 GB).
	f, err := excelize.OpenReader(r, excelize.Options{UnzipSizeLimit: maxUnzipBytes})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, ErrEmpty
	}
	lines, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	res, err := parseLines(lines)
	if err != nil {
		return nil, err
	}
	res.Sheet = sheets[0]
	return res, nil
}

func parseLines(lines [][]string) (*Result, error) {
	header := -1
	for i, l := range lines {
		if !isBlank(l) {
			header = i
			break
		}
	}
	if header < 0 {
		return nil, ErrEmpty
	}

	idx, err := mapColumns(lines[header])
	if err != nil {
		return nil, err
	}

	res := &Result{}
	parentNames := map[string]string{} // email -> parent name key of first row
	seen := map[string]bool{}          // email + child key

	for i := header + 1; i < len(lines); i++ {
		l := lines[i]
		if isBlank(l) {
			continue
		}
		res.RowsRead++
		line := i + 1

		row := Row{
			Line:       line,
			ParentName: textnorm.Clean(cell(l, idx[ColParentName])),
			ChildName:  textnorm.Clean(cell(l, idx[ColChildName])),
			Email:      normalizeEmail(cell(l, idx[ColEmail])),
		}

		if issues := validate(row); len(issues) > 0 {
			res.Errors = append(res.Errors, issues...)
			continue
		}

		key := row.Email + "\x00" + textnorm.Key(row.ChildName)
		if seen[key] {
			res.Warnings = append(res.Warnings, Issue{Line: line, Code: IssueDuplicateRow, Value: row.ChildName})
			continue
		}
		seen[key] = true

		pKey := textnorm.Key(row.ParentName)
		if first, ok := parentNames[row.Email]; ok && first != pKey {
			res.Warnings = append(res.Warnings, Issue{Line: line, Code: IssueParentNameConflict, Value: row.ParentName})
		} else if !ok {
			parentNames[row.Email] = pKey
		}

		res.Rows = append(res.Rows, row)
	}

	if res.RowsRead == 0 {
		return nil, ErrEmpty
	}
	return res, nil
}

func mapColumns(header []string) (map[Column]int, error) {
	idx := map[Column]int{}
	for i, h := range header {
		col, ok := headerAliases[textnorm.Key(h)]
		if !ok {
			continue
		}
		if _, dup := idx[col]; !dup {
			idx[col] = i
		}
	}

	var missing []Column
	for _, c := range columns {
		if _, ok := idx[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, &MissingColumnsError{Columns: missing}
	}
	return idx, nil
}

func validate(r Row) []Issue {
	var issues []Issue
	if r.ParentName == "" {
		issues = append(issues, Issue{Line: r.Line, Code: IssueMissingParentName})
	}
	if r.ChildName == "" {
		issues = append(issues, Issue{Line: r.Line, Code: IssueMissingChildName})
	}
	switch {
	case r.Email == "":
		issues = append(issues, Issue{Line: r.Line, Code: IssueMissingEmail})
	case !validEmail(r.Email):
		issues = append(issues, Issue{Line: r.Line, Code: IssueInvalidEmail, Value: r.Email})
	}
	return issues
}

func normalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.ToLower(s), "mailto:")
	return s
}

// validEmail accepts a bare address only ("a@b.fr", not "Name <a@b.fr>").
func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && strings.Contains(s[at+1:], ".")
}

func cell(l []string, i int) string {
	if i < len(l) {
		return l[i]
	}
	return ""
}

func isBlank(l []string) bool {
	for _, c := range l {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
