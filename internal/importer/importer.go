// Package importer reads the families file (.xlsx or .csv) and validates its
// rows. It does not touch the database: see store.ImportFamilies for that.
package importer

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"cafeteria-management/internal/textnorm"
)

// maxUnzipBytes is far above any real families file.
const maxUnzipBytes = 200 << 20

// Limits far above real files (a school has a few thousand lines, the export
// 28 columns): they bound memory on crafted files.
const (
	maxRecords = 50000 // non-blank lines after the header
	maxFields  = 200   // columns per line

	maxFamilyNumberLen = 64  // runes
	maxBillingNameLen  = 255 // runes
)

// Column is one of the columns the file must contain.
type Column string

// Required columns of the parent/child layout (SPEC §4.1).
const (
	ColParentName Column = "parent_name"
	ColChildName  Column = "child_name"
	ColEmail      Column = "email"
)

// Required columns of the family layout, the CSV export (SPEC §4.1.1).
const (
	ColFamilyNumber Column = "family_number"
	ColBillingName  Column = "billing_name"
	ColPayerEmail   Column = "payer_email"
)

var (
	childColumns  = []Column{ColParentName, ColChildName, ColEmail}
	familyColumns = []Column{ColFamilyNumber, ColBillingName, ColPayerEmail}
)

// Layout is the kind of data a file holds, chosen from its header row.
type Layout int

// Layouts.
const (
	// LayoutChildren: one row per parent/child pair (SPEC §4.1).
	LayoutChildren Layout = iota
	// LayoutFamilies: the families export, one entry per family number and
	// payer email (SPEC §4.1.1).
	LayoutFamilies
)

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

	// RL (guardian) and phone headers are deliberately absent: only the
	// payer email is read (data minimisation, SPEC §4.1.1).
	"numfam":        ColFamilyNumber,
	"nomfact":       ColBillingName,
	"email payeur":  ColPayerEmail,
	"mail payeur":   ColPayerEmail,
	"e-mail payeur": ColPayerEmail,
}

// maxInvalidEmailLen is the RFC 5321 address limit: a broken cell cannot
// bloat the pages that show the rejected value.
const maxInvalidEmailLen = 254

// Errors that make the whole file unusable.
var (
	ErrUnreadable = errors.New("file is not readable")
	ErrEmpty      = errors.New("file contains no data")
	ErrTooLarge   = errors.New("file is too large")
	// ErrTooManyColumns: a line has far more columns than any real export.
	ErrTooManyColumns = errors.New("file has too many columns")
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

	// Family layout: the line cannot be identified or named.
	IssueMissingFamilyNumber IssueCode = "missing_family_number"
	IssueMissingBillingName  IssueCode = "missing_billing_name"
	// IssueValueTooLong: Value is the column header (NUMFAM or NOMFACT).
	IssueValueTooLong IssueCode = "value_too_long"
)

// Warnings: the file is usable, but the user should know.
const (
	// IssueDuplicateRow: same child and email as an earlier row; ignored.
	IssueDuplicateRow IssueCode = "duplicate_row"
	// IssueParentNameConflict: same email as an earlier row but another
	// parent name; the first name is kept.
	IssueParentNameConflict IssueCode = "parent_name_conflict"
	// IssueBillingNameConflict: same family and payer email as an earlier
	// line of the same entry but another billing name; the first is kept.
	IssueBillingNameConflict IssueCode = "billing_name_conflict"
	// IssueMissingPayerEmail: the payer email is empty (Value: family number).
	// The family is imported but cannot be emailed.
	IssueMissingPayerEmail IssueCode = "missing_payer_email"
	// IssueInvalidPayerEmail: the payer email is not a valid address (Value:
	// the cleaned value). The family is imported but cannot be emailed there.
	IssueInvalidPayerEmail IssueCode = "invalid_payer_email"
)

// Issue is a problem found on one line of the sheet.
type Issue struct {
	Line  int // 1-based line number, as shown in Excel
	Code  IssueCode
	Value string // offending value, if any
}

// Row is a valid line of the file. Values are cleaned; Email is lower-case.
//
// In the family layout a Row is an entry: ParentName and ChildName are both
// the billing name (NOMFACT), and Email == "" means the entry has no valid
// payer email (InvalidEmail is then the rejected value, or "" if empty).
type Row struct {
	Line         int
	ParentName   string
	ChildName    string
	Email        string
	FamilyNumber string
	InvalidEmail string
}

// Result is the outcome of parsing a file.
type Result struct {
	Sheet    string // empty for a CSV file
	Layout   Layout
	RowsRead int     // non-empty data lines
	Rows     []Row   // valid, de-duplicated rows, ordered by Line
	Errors   []Issue // invalid lines; the file must be fixed before import
	Warnings []Issue
}

// Families reports whether the file uses the family layout.
func (r *Result) Families() bool { return r.Layout == LayoutFamilies }

// record is one line of a file: its 1-based number as shown to the user and
// its cells.
type record struct {
	Line  int
	Cells []string
}

// ParseXLSX reads the first sheet of an .xlsx file. The first non-empty line
// is the header; other columns than the required ones are ignored.
func ParseXLSX(r io.Reader) (*Result, error) {
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
	records := make([]record, len(lines))
	for i, l := range lines {
		records[i] = record{Line: i + 1, Cells: l}
	}
	res, err := parseRecords(records)
	if err != nil {
		return nil, err
	}
	res.Sheet = sheets[0]
	return res, nil
}

// parseRecords picks the layout from the header (first non-empty record) and
// applies its rules. Excel and CSV share it.
func parseRecords(records []record) (*Result, error) {
	header := -1
	for i, rec := range records {
		if !isBlank(rec.Cells) {
			header = i
			break
		}
	}
	if header < 0 {
		return nil, ErrEmpty
	}

	layout, idx, err := mapColumns(records[header].Cells)
	if err != nil {
		return nil, err
	}

	data := records[header+1:]
	nonBlank := 0
	for _, rec := range data {
		if !isBlank(rec.Cells) {
			nonBlank++
		}
	}
	if nonBlank > maxRecords {
		return nil, ErrTooLarge
	}
	var res *Result
	if layout == LayoutFamilies {
		res = parseFamilies(data, idx)
	} else {
		res = parseChildren(data, idx)
	}
	res.Layout = layout
	if res.RowsRead == 0 {
		return nil, ErrEmpty
	}
	return res, nil
}

// parseChildren applies the parent/child rules (SPEC §4.1).
func parseChildren(data []record, idx map[Column]int) *Result {
	res := &Result{}
	parentNames := map[string]string{} // email -> parent name key of first row
	seen := map[string]bool{}          // email + child key

	for _, rec := range data {
		l := rec.Cells
		if isBlank(l) {
			continue
		}
		res.RowsRead++
		line := rec.Line

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
	return res
}

// familyLine is a valid-enough line of the family export.
type familyLine struct {
	line    int
	name    string // billing name
	email   string // valid payer email, or ""
	invalid string // rejected non-empty payer email, or ""
}

// parseFamilies applies the family rules (SPEC §4.1.1): an entry is a
// (family number, valid payer email) pair, plus one "no valid email" entry
// for a family with an unusable payer email.
func parseFamilies(data []record, idx map[Column]int) *Result {
	res := &Result{}
	lines := map[string][]familyLine{} // by family number
	var order []string                 // family numbers, first appearance

	// Pass 1: per line.
	for _, rec := range data {
		l := rec.Cells
		if isBlank(l) {
			continue
		}
		res.RowsRead++

		num := textnorm.Clean(cell(l, idx[ColFamilyNumber]))
		name := textnorm.Clean(cell(l, idx[ColBillingName]))
		if num == "" {
			res.Errors = append(res.Errors, Issue{Line: rec.Line, Code: IssueMissingFamilyNumber})
		}
		if name == "" {
			res.Errors = append(res.Errors, Issue{Line: rec.Line, Code: IssueMissingBillingName})
		}
		if utf8.RuneCountInString(num) > maxFamilyNumberLen {
			res.Errors = append(res.Errors, Issue{Line: rec.Line, Code: IssueValueTooLong, Value: "NUMFAM"})
		}
		if utf8.RuneCountInString(name) > maxBillingNameLen {
			res.Errors = append(res.Errors, Issue{Line: rec.Line, Code: IssueValueTooLong, Value: "NOMFACT"})
		}
		if num == "" || name == "" || utf8.RuneCountInString(num) > maxFamilyNumberLen || utf8.RuneCountInString(name) > maxBillingNameLen {
			continue
		}

		fl := familyLine{line: rec.Line, name: name}
		switch email := normalizeEmail(cell(l, idx[ColPayerEmail])); {
		case email == "":
			res.Warnings = append(res.Warnings, Issue{Line: rec.Line, Code: IssueMissingPayerEmail, Value: num})
		case validEmail(email):
			fl.email = email
		default:
			fl.invalid = cut(textnorm.Clean(email), maxInvalidEmailLen)
			res.Warnings = append(res.Warnings, Issue{Line: rec.Line, Code: IssueInvalidPayerEmail, Value: fl.invalid})
		}

		if _, ok := lines[num]; !ok {
			order = append(order, num)
		}
		lines[num] = append(lines[num], fl)
	}

	// Pass 2: per family.
	for _, num := range order {
		fam := lines[num]

		byEmail := map[string]*Row{}
		var noEmail []familyLine // the lines making up the no-email entry
		var invalidLines, emptyLines []familyLine
		for _, fl := range fam {
			switch {
			case fl.email != "":
				e, ok := byEmail[fl.email]
				if !ok {
					row := Row{Line: fl.line, ParentName: fl.name, ChildName: fl.name,
						Email: fl.email, FamilyNumber: num}
					byEmail[fl.email] = &row
					res.Rows = append(res.Rows, row)
					continue
				}
				if textnorm.Key(e.ChildName) != textnorm.Key(fl.name) {
					res.Warnings = append(res.Warnings, Issue{Line: fl.line, Code: IssueBillingNameConflict, Value: fl.name})
				}
			case fl.invalid != "":
				invalidLines = append(invalidLines, fl)
			default:
				emptyLines = append(emptyLines, fl)
			}
		}

		switch {
		case len(invalidLines) > 0:
			noEmail = invalidLines
		case len(byEmail) == 0:
			noEmail = emptyLines
		}
		if len(noEmail) > 0 {
			first := noEmail[0]
			res.Rows = append(res.Rows, Row{Line: first.line, ParentName: first.name, ChildName: first.name,
				FamilyNumber: num, InvalidEmail: first.invalid})
			for _, fl := range noEmail[1:] {
				if textnorm.Key(first.name) != textnorm.Key(fl.name) {
					res.Warnings = append(res.Warnings, Issue{Line: fl.line, Code: IssueBillingNameConflict, Value: fl.name})
				}
			}
		}
	}

	sort.SliceStable(res.Rows, func(i, j int) bool { return res.Rows[i].Line < res.Rows[j].Line })

	// The same address in another family keeps the first parent name.
	parentNames := map[string]string{}
	for _, row := range res.Rows {
		if row.Email == "" {
			continue
		}
		key := textnorm.Key(row.ParentName)
		if first, ok := parentNames[row.Email]; !ok {
			parentNames[row.Email] = key
		} else if first != key {
			res.Warnings = append(res.Warnings, Issue{Line: row.Line, Code: IssueParentNameConflict, Value: row.ParentName})
		}
	}
	sort.SliceStable(res.Warnings, func(i, j int) bool { return res.Warnings[i].Line < res.Warnings[j].Line })
	return res
}

// mapColumns maps the header cells to columns and chooses the layout: the
// family layout as soon as the header has one of its columns. Empty and
// unknown headers are ignored; the first duplicate wins.
func mapColumns(header []string) (Layout, map[Column]int, error) {
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

	layout, required := LayoutChildren, childColumns
	for _, c := range familyColumns {
		if _, ok := idx[c]; ok {
			layout, required = LayoutFamilies, familyColumns
			break
		}
	}

	var missing []Column
	for _, c := range required {
		if _, ok := idx[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return layout, nil, &MissingColumnsError{Columns: missing}
	}
	return layout, idx, nil
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

// cut shortens s to at most n runes, ending with "…" when it was cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
