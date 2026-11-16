package importer

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// xlsx builds an in-memory Excel file whose first sheet holds lines.
func xlsx(t *testing.T, lines [][]any) *bytes.Buffer {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for i, l := range lines {
		cellRef, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetSheetRow("Sheet1", cellRef, &l); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestParseValidFile(t *testing.T) {
	buf := xlsx(t, [][]any{
		{},
		{"Nom Parent", "Classe", "Nom Enfant", "Adresse E-mail"},
		{"  Marie  Dupont ", "CP", "Léa Dupont", " Marie.Dupont@Example.FR "},
		{"Marie Dupont", "CE1", "Tom Dupont", "marie.dupont@example.fr"},
		{},
		{"Paul Martin", "CP", "Zoé Martin", "mailto:paul@example.fr"},
	})

	res, err := Parse(buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsRead != 3 || len(res.Rows) != 3 || len(res.Errors) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("got read=%d rows=%d errors=%v warnings=%v", res.RowsRead, len(res.Rows), res.Errors, res.Warnings)
	}

	want := Row{Line: 3, ParentName: "Marie Dupont", ChildName: "Léa Dupont", Email: "marie.dupont@example.fr"}
	if res.Rows[0] != want {
		t.Errorf("row 0 = %+v, want %+v", res.Rows[0], want)
	}
	if res.Rows[2].Email != "paul@example.fr" {
		t.Errorf("mailto: not removed: %q", res.Rows[2].Email)
	}
}

func TestParseIssues(t *testing.T) {
	buf := xlsx(t, [][]any{
		{"nom parent", "nom enfant", "email"},
		{"", "Léa", "a@example.fr"},            // 2: missing parent
		{"Jean", "", "a@example.fr"},           // 3: missing child
		{"Jean", "Léa", ""},                    // 4: missing email
		{"Jean", "Léa", "not-an-email"},        // 5: invalid email
		{"Jean", "Léa", "Jean <j@example.fr>"}, // 6: invalid email
		{"Jean", "Léa", "j@example.fr"},        // 7: ok
		{"Jean", "LEA", "j@example.fr"},        // 8: duplicate of 7
		{"Jeanne", "Tom", "j@example.fr"},      // 9: parent name conflict
	})

	res, err := Parse(buf)
	if err != nil {
		t.Fatal(err)
	}

	wantErrors := []Issue{
		{Line: 2, Code: IssueMissingParentName},
		{Line: 3, Code: IssueMissingChildName},
		{Line: 4, Code: IssueMissingEmail},
		{Line: 5, Code: IssueInvalidEmail, Value: "not-an-email"},
		{Line: 6, Code: IssueInvalidEmail, Value: "jean <j@example.fr>"},
	}
	if len(res.Errors) != len(wantErrors) {
		t.Fatalf("errors = %+v", res.Errors)
	}
	for i, w := range wantErrors {
		if res.Errors[i] != w {
			t.Errorf("error %d = %+v, want %+v", i, res.Errors[i], w)
		}
	}

	wantWarnings := []Issue{
		{Line: 8, Code: IssueDuplicateRow, Value: "LEA"},
		{Line: 9, Code: IssueParentNameConflict, Value: "Jeanne"},
	}
	if len(res.Warnings) != len(wantWarnings) {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	for i, w := range wantWarnings {
		if res.Warnings[i] != w {
			t.Errorf("warning %d = %+v, want %+v", i, res.Warnings[i], w)
		}
	}

	if len(res.Rows) != 2 || res.RowsRead != 8 {
		t.Errorf("rows=%d read=%d", len(res.Rows), res.RowsRead)
	}
}

func TestParseFileErrors(t *testing.T) {
	t.Run("not excel", func(t *testing.T) {
		_, err := Parse(strings.NewReader("hello"))
		if !errors.Is(err, ErrUnreadable) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, err := Parse(xlsx(t, nil))
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("header only", func(t *testing.T) {
		_, err := Parse(xlsx(t, [][]any{{"nom parent", "nom enfant", "email"}}))
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing columns", func(t *testing.T) {
		_, err := Parse(xlsx(t, [][]any{{"nom parent", "classe"}, {"Jean", "CP"}}))
		var mc *MissingColumnsError
		if !errors.As(err, &mc) {
			t.Fatalf("err = %v", err)
		}
		if len(mc.Columns) != 2 || mc.Columns[0] != ColChildName || mc.Columns[1] != ColEmail {
			t.Errorf("missing = %v", mc.Columns)
		}
	})
}
