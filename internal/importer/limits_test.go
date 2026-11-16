package importer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const limitsHeader = "NUMFAM;NOMFACT;EMAIL PAYEUR\n"

func parseCSVErr(s string) (*Result, error) {
	return ParseCSV(strings.NewReader(s))
}

func TestParseCSVManyBlankLines(t *testing.T) {
	t.Run("only blank lines", func(t *testing.T) {
		_, err := parseCSVErr(strings.Repeat(";\n", 3<<20))
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("err = %v, want ErrEmpty", err)
		}
	})
	t.Run("header then blank lines", func(t *testing.T) {
		_, err := parseCSVErr(limitsHeader + strings.Repeat(";;\n", 1<<20))
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("err = %v, want ErrEmpty", err)
		}
	})
}

func TestParseCSVColumnLimit(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		wantOK bool
	}{
		// 199 delimiters = 200 columns = maxFields. 200 delimiters pass the
		// pre-scan (> maxFields) but the 201 fields are refused by the reader.
		{"200 columns on header", "NUMFAM;NOMFACT;EMAIL PAYEUR" + strings.Repeat(";", 197) + "\n1;A;a@example.fr\n", true},
		{"201 columns on header", "NUMFAM;NOMFACT;EMAIL PAYEUR" + strings.Repeat(";", 198) + "\n1;A;a@example.fr\n", false},
		{"200 columns on data line", limitsHeader + "1;A;a@example.fr" + strings.Repeat(";", 197) + "\n", true},
		{"201 columns on data line", limitsHeader + "1;A;a@example.fr" + strings.Repeat(";", 198) + "\n", false},
		{"201 delimiters on data line", limitsHeader + "1;A;a@example.fr" + strings.Repeat(";", 199) + "\n", false},
		{"one huge line", strings.Repeat(";", 5<<20) + "\n", false},
		{"201 delimiters comma file", "NUMFAM,NOMFACT,EMAIL PAYEUR" + strings.Repeat(",", 199) + "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCSVErr(tt.text)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrTooManyColumns) {
				t.Fatalf("err = %v, want ErrTooManyColumns", err)
			}
		})
	}
}

// csvRows builds a family CSV with n distinct, valid data rows.
func csvRows(n int) string {
	var b strings.Builder
	b.WriteString(limitsHeader)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%d;N%d;f%d@example.fr\n", i, i, i)
	}
	return b.String()
}

func TestParseCSVRecordLimit(t *testing.T) {
	t.Run("exactly maxRecords", func(t *testing.T) {
		res, err := parseCSVErr(csvRows(maxRecords))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if res.RowsRead != maxRecords {
			t.Errorf("RowsRead = %d", res.RowsRead)
		}
	})
	t.Run("maxRecords+1", func(t *testing.T) {
		_, err := parseCSVErr(csvRows(maxRecords + 1))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	})
	t.Run("blank lines do not count", func(t *testing.T) {
		text := csvRows(maxRecords) + strings.Repeat(";;\n", 1000)
		if _, err := parseCSVErr(text); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

// The XLSX path goes through parseRecords after excelize has read the sheet.
func TestParseRecordsLimit(t *testing.T) {
	build := func(n int) []record {
		recs := []record{{Line: 1, Cells: []string{"NUMFAM", "NOMFACT", "EMAIL PAYEUR"}}}
		for i := 0; i < n; i++ {
			recs = append(recs, record{Line: i + 2, Cells: []string{fmt.Sprint(i), "N", "a@example.fr"}})
		}
		return recs
	}
	if _, err := parseRecords(build(maxRecords)); err != nil {
		t.Fatalf("maxRecords rows: %v", err)
	}
	if _, err := parseRecords(build(maxRecords + 1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("maxRecords+1 rows: err = %v, want ErrTooLarge", err)
	}
	// Blank records are not counted.
	recs := build(maxRecords)
	for i := 0; i < 10; i++ {
		recs = append(recs, record{Line: 100000 + i, Cells: []string{"", "", ""}})
	}
	if _, err := parseRecords(recs); err != nil {
		t.Fatalf("with blank records: %v", err)
	}
}

func TestParseXLSXRecordLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a big workbook")
	}
	lines := [][]any{{"NUMFAM", "NOMFACT", "EMAIL PAYEUR"}}
	for i := 0; i < maxRecords+1; i++ {
		lines = append(lines, []any{i, "N", "a@example.fr"})
	}
	_, err := ParseXLSX(xlsx(t, lines))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestValueTooLong(t *testing.T) {
	e64, e65 := strings.Repeat("é", 64), strings.Repeat("é", 65)
	e255, e256 := strings.Repeat("é", 255), strings.Repeat("é", 256)

	t.Run("limits ok", func(t *testing.T) {
		res := parseCSVString(t, limitsHeader+e64+";"+e255+";a@example.fr\n")
		if len(res.Errors) != 0 || len(res.Rows) != 1 {
			t.Fatalf("errors=%+v rows=%d", res.Errors, len(res.Rows))
		}
		if res.Rows[0].FamilyNumber != e64 || res.Rows[0].ChildName != e255 {
			t.Error("stored values altered")
		}
	})

	// Byte length of these values exceeds the limit, rune count does not.
	if len(e64) <= maxFamilyNumberLen || len(e255) <= maxBillingNameLen {
		t.Fatal("test values must be multi-byte")
	}

	tests := []struct {
		name  string
		num   string
		bill  string
		want  []Issue
		nRows int
	}{
		{"NUMFAM 65", e65, "A", []Issue{{Line: 3, Code: IssueValueTooLong, Value: "NUMFAM"}}, 1},
		{"NOMFACT 256", "2", e256, []Issue{{Line: 3, Code: IssueValueTooLong, Value: "NOMFACT"}}, 1},
		{"both", e65, e256, []Issue{
			{Line: 3, Code: IssueValueTooLong, Value: "NUMFAM"},
			{Line: 3, Code: IssueValueTooLong, Value: "NOMFACT"},
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := limitsHeader + "1;OK;ok@example.fr\n" + tt.num + ";" + tt.bill + ";bad@example.fr\n"
			res := parseCSVString(t, text)
			if len(res.Errors) != len(tt.want) {
				t.Fatalf("errors = %+v", res.Errors)
			}
			for i, w := range tt.want {
				if res.Errors[i] != w {
					t.Errorf("error %d = %+v, want %+v", i, res.Errors[i], w)
				}
			}
			for _, e := range res.Errors {
				if strings.Contains(e.Value, "é") {
					t.Errorf("issue leaks file data: %q", e.Value)
				}
			}
			if len(res.Rows) != tt.nRows {
				t.Errorf("rows = %d, want %d (long line must not be stored)", len(res.Rows), tt.nRows)
			}
		})
	}
}

func TestParseCSVReuseRecordKeepsValues(t *testing.T) {
	// Blank lines between records, extra unmapped columns, a multi-line cell.
	var b strings.Builder
	b.WriteString("\n;;\nEXTRA;NUMFAM;NOMFACT;EMAIL PAYEUR;X\n")
	type fam struct{ num, name, email string }
	var fams []fam
	wantLine := map[string]int{}
	line := 4
	for i := 0; i < 30; i++ {
		f := fam{fmt.Sprint(1000 + i), fmt.Sprintf("Élodie-%d DUPONT", i), fmt.Sprintf("p%d@example.fr", i)}
		fams = append(fams, f)
		wantLine[f.num] = line
		fmt.Fprintf(&b, "junk%d;%s;%s;%s;zzz\n", i, f.num, f.name, strings.ToUpper(f.email))
		line++
		if i%3 == 0 {
			b.WriteString(";;;;\n\n")
			line += 2
		}
	}
	res := parseCSVString(t, b.String())
	if len(res.Rows) != len(fams) || len(res.Errors) != 0 {
		t.Fatalf("rows=%d errors=%+v", len(res.Rows), res.Errors)
	}
	for i, f := range fams {
		r := res.Rows[i]
		if r.FamilyNumber != f.num || r.ChildName != f.name || r.ParentName != f.name || r.Email != f.email {
			t.Errorf("row %d = %+v, want %+v", i, r, f)
		}
		if r.Line != wantLine[f.num] {
			t.Errorf("row %d line = %d, want %d", i, r.Line, wantLine[f.num])
		}
	}
}
