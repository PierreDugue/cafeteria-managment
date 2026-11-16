package importer

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Fake RL / phone values: they must never show up in a Result.
const (
	fakeRLEmail = "rl1.secret@example.org"
	fakeRLName  = "RLPRENOMSECRET"
	fakePhone   = "0611223344"
)

// exportHeader mimics the real export: empty header cells, RL columns and
// the payer columns at the end.
func exportHeader() []string {
	h := make([]string, 28)
	h[0], h[1], h[2] = "NUMFAM", "NUMFACFACT", "NOMFACT"
	for i, n := range []string{"NOM RL1", "PRENOM RL1", "PORT RL1", "EMAIL RL1", "X1", "NOM RL2", "PRENOM RL2", "PORT RL2", "EMAIL RL2", "X2"} {
		if strings.HasPrefix(n, "X") {
			continue // stays empty
		}
		h[13+i] = n
	}
	h[26], h[27] = "PORT PAYEUR", "EMAIL PAYEUR"
	return h
}

// exportLine builds one fake line of the export.
func exportLine(num, name, email string) []string {
	l := make([]string, 28)
	l[0], l[1], l[2] = num, "F"+num, name
	l[13], l[14], l[15], l[16] = fakeRLName, "Pierre", fakePhone, fakeRLEmail
	l[26], l[27] = fakePhone, email
	return l
}

func joinCSV(sep string, lines ...[]string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strings.Join(l, sep))
		b.WriteString("\n")
	}
	return b.String()
}

// famCSV builds a CSV with the export header and the given lines.
func famCSV(lines ...[]string) string {
	return joinCSV(";", append([][]string{exportHeader()}, lines...)...)
}

func parseCSVString(t *testing.T, s string) *Result {
	t.Helper()
	res, err := ParseCSV(strings.NewReader(s))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	return res
}

func TestParseCSVExportHeader(t *testing.T) {
	res := parseCSVString(t, famCSV(
		exportLine("100", "DUPONT Marie", "Marie@Example.FR"),
		exportLine("101", "MARTIN Paul", "paul@example.fr"),
	))
	if res.Layout != LayoutFamilies || !res.Families() || res.Sheet != "" {
		t.Fatalf("layout=%v sheet=%q", res.Layout, res.Sheet)
	}
	want := []Row{
		{Line: 2, ParentName: "DUPONT Marie", ChildName: "DUPONT Marie", Email: "marie@example.fr", FamilyNumber: "100"},
		{Line: 3, ParentName: "MARTIN Paul", ChildName: "MARTIN Paul", Email: "paul@example.fr", FamilyNumber: "101"},
	}
	if len(res.Rows) != 2 || res.Rows[0] != want[0] || res.Rows[1] != want[1] {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.RowsRead != 2 || len(res.Errors) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("read=%d errors=%v warnings=%v", res.RowsRead, res.Errors, res.Warnings)
	}
}

func TestParseCSVNeverReadsRLOrPhones(t *testing.T) {
	res := parseCSVString(t, famCSV(
		exportLine("100", "DUPONT Marie", "marie@example.fr"),
		exportLine("101", "MARTIN Paul", "pas un email"),
		exportLine("102", "DURAND Zoé", ""),
	))
	dump := fmt.Sprintf("%+v %+v %+v", res.Rows, res.Errors, res.Warnings)
	for _, secret := range []string{fakeRLEmail, fakeRLName, fakePhone, "Pierre", "F100"} {
		if strings.Contains(dump, secret) {
			t.Errorf("result contains %q: %s", secret, dump)
		}
	}
}

func TestParseCSVShuffledColumnsAndCase(t *testing.T) {
	// Positions irrelevant, header matched ignoring case and accents, empty
	// and unknown headers ignored.
	in := "Mail Payeur;;ÑOTHER;nomfact;NumFam\n" +
		"a@example.fr;;x;DUPONT;7\n"
	res := parseCSVString(t, in)
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := Row{Line: 2, ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.fr", FamilyNumber: "7"}
	if res.Rows[0] != want {
		t.Errorf("row = %+v, want %+v", res.Rows[0], want)
	}

	res = parseCSVString(t, "E-MAIL PAYEUR;NOMFACT;NUMFAM\nb@example.fr;X;1\n")
	if len(res.Rows) != 1 || res.Rows[0].Email != "b@example.fr" {
		t.Errorf("rows = %+v", res.Rows)
	}
}

func TestParseCSVEncodings(t *testing.T) {
	const content = "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;ÉLODIE Müller-Hélène;e@example.fr\n"
	const want = "ÉLODIE Müller-Hélène"

	win1252, err := charmap.Windows1252.NewEncoder().String(content)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.ValidString(win1252) {
		t.Fatal("fixture should not be valid UTF-8")
	}
	if !strings.Contains(win1252, "\xC9") || !strings.Contains(win1252, "\xE9") {
		t.Fatal("fixture should hold 0xC9 and 0xE9")
	}

	tests := []struct {
		name string
		raw  string
	}{
		{"utf-8", content},
		{"utf-8 with BOM", "\xEF\xBB\xBF" + content},
		{"windows-1252", win1252},
		{"windows-1252 CRLF", strings.ReplaceAll(win1252, "\n", "\r\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parseCSVString(t, tt.raw)
			if len(res.Rows) != 1 || res.Rows[0].ChildName != want || res.Rows[0].FamilyNumber != "1" {
				t.Fatalf("rows = %+v", res.Rows)
			}
		})
	}
}

func TestParseCSVFileErrors(t *testing.T) {
	big := "NUMFAM;NOMFACT;EMAIL PAYEUR\n" + strings.Repeat("1;A;a@example.fr\n", maxCSVBytes/10)
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"NUL byte", "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;A\x00;a@example.fr\n", ErrUnreadable},
		{"UTF-16 like", "N\x00U\x00M\x00", ErrUnreadable},
		{"empty", "", ErrEmpty},
		{"blank lines only", "\n\n  \n", ErrEmpty},
		{"header only", "NUMFAM;NOMFACT;EMAIL PAYEUR\n", ErrEmpty},
		{"header and blank lines", "NUMFAM;NOMFACT;EMAIL PAYEUR\n;;\n\n", ErrEmpty},
		{"too large", big, ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCSV(strings.NewReader(tt.in))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("exactly maxCSVBytes is accepted", func(t *testing.T) {
		head := "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;A;a@example.fr\n"
		in := head + strings.Repeat(" ", maxCSVBytes-len(head))
		if _, err := ParseCSV(strings.NewReader(in)); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("one byte more is refused", func(t *testing.T) {
		head := "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;A;a@example.fr\n"
		in := head + strings.Repeat(" ", maxCSVBytes-len(head)+1)
		if _, err := ParseCSV(strings.NewReader(in)); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

func TestParseCSVReadError(t *testing.T) {
	if _, err := ParseCSV(failingReader{}); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCSVMissingColumns(t *testing.T) {
	var mc *MissingColumnsError

	_, err := ParseCSV(strings.NewReader("NUMFAM;NOMFACT\n1;A\n"))
	if !errors.As(err, &mc) || len(mc.Columns) != 1 || mc.Columns[0] != ColPayerEmail {
		t.Fatalf("err = %v", err)
	}

	_, err = ParseCSV(strings.NewReader("EMAIL PAYEUR\na@example.fr\n"))
	if !errors.As(err, &mc) || len(mc.Columns) != 2 || mc.Columns[0] != ColFamilyNumber || mc.Columns[1] != ColBillingName {
		t.Fatalf("err = %v", err)
	}

	// Neither layout: the child layout's columns are reported.
	_, err = ParseCSV(strings.NewReader("foo;bar\n1;2\n"))
	if !errors.As(err, &mc) || len(mc.Columns) != 3 || mc.Columns[0] != ColParentName {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCSVDelimiters(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"semicolon", "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;DUPONT;a@example.fr\n"},
		{"comma", "NUMFAM,NOMFACT,EMAIL PAYEUR\n1,DUPONT,a@example.fr\n"},
		{"tie goes to semicolon", "EMAIL PAYEUR;NUMFAM;NOMFACT;,,,\na@example.fr;1;DUPONT;\n"},
		{"leading blank line", "\n\nNUMFAM,NOMFACT,EMAIL PAYEUR\n1,DUPONT,a@example.fr\n"},
		{"CRLF", "NUMFAM;NOMFACT;EMAIL PAYEUR\r\n1;DUPONT;a@example.fr\r\n"},
		{"no final newline", "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;DUPONT;a@example.fr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parseCSVString(t, tt.in)
			if len(res.Rows) != 1 || res.Rows[0].ChildName != "DUPONT" || res.Rows[0].Email != "a@example.fr" {
				t.Fatalf("rows = %+v", res.Rows)
			}
		})
	}

	t.Run("detectDelimiter", func(t *testing.T) {
		for in, want := range map[string]rune{
			"a;b;c": ';', "a,b,c": ',', "a;b,c": ';', "a,b;c,d": ',', "": ';', "\n\na,b": ',', "x": ';',
		} {
			if got := detectDelimiter(in); got != want {
				t.Errorf("detectDelimiter(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func TestParseCSVQuoting(t *testing.T) {
	t.Run("quoted separator and escaped quote", func(t *testing.T) {
		res := parseCSVString(t, "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;\"DUPONT; \"\"Marie\"\"\";a@example.fr\n")
		if len(res.Rows) != 1 || res.Rows[0].ChildName != `DUPONT; "Marie"` {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("quoted line break", func(t *testing.T) {
		res := parseCSVString(t, "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;\"DUPONT\nMarie\";a@example.fr\n")
		if len(res.Rows) != 1 || res.Rows[0].ChildName != "DUPONT Marie" {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("stray quote is tolerated", func(t *testing.T) {
		res := parseCSVString(t, "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;DUP\"ONT;a@example.fr\n")
		if len(res.Rows) != 1 || res.Rows[0].Email != "a@example.fr" {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("comma delimiter with quoted commas", func(t *testing.T) {
		res := parseCSVString(t, "NUMFAM,NOMFACT,EMAIL PAYEUR\n1,\"DUPONT, Marie\",a@example.fr\n")
		if len(res.Rows) != 1 || res.Rows[0].ChildName != "DUPONT, Marie" {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
}

func TestParseCSVRaggedLines(t *testing.T) {
	// Fewer cells than the header: the payer email is simply missing.
	res := parseCSVString(t, "NUMFAM;NOMFACT;EMAIL PAYEUR\n1;DUPONT\n2;MARTIN;m@example.fr;extra;cells\n")
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.Rows[0].Email != "" || res.Rows[1].Email != "m@example.fr" {
		t.Errorf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != IssueMissingPayerEmail || res.Warnings[0].Line != 2 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestParseCSVLineNumbers(t *testing.T) {
	in := "\n" + // 1: blank
		"NUMFAM;NOMFACT;EMAIL PAYEUR\n" + // 2: header
		"\n" + // 3: blank
		";A;a@example.fr\n" + // 4: missing NUMFAM
		"2;\"multi\nline\nname\";b@example.fr\n" + // 5-7
		"3;;c@example.fr\n" + // 8: missing NOMFACT
		"4;D;not-an-email\n" // 9
	res := parseCSVString(t, in)

	wantErrors := []Issue{
		{Line: 4, Code: IssueMissingFamilyNumber},
		{Line: 8, Code: IssueMissingBillingName},
	}
	if len(res.Errors) != 2 || res.Errors[0] != wantErrors[0] || res.Errors[1] != wantErrors[1] {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if len(res.Rows) != 2 || res.Rows[0].Line != 5 || res.Rows[1].Line != 9 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != (Issue{Line: 9, Code: IssueInvalidPayerEmail, Value: "not-an-email"}) {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	if res.RowsRead != 4 {
		t.Errorf("RowsRead = %d, want 4", res.RowsRead)
	}
}

func TestParseCSVChildLayout(t *testing.T) {
	in := "nom parent;nom enfant;email\n" +
		"Marie Dupont;Léa;marie@example.fr\n" +
		"Marie Dupont;LEA;marie@example.fr\n" +
		"Jean;Tom;pas-un-email\n" +
		";Zoé;z@example.fr\n"
	res := parseCSVString(t, in)
	if res.Layout != LayoutChildren || res.Families() {
		t.Fatalf("layout = %v", res.Layout)
	}
	if len(res.Rows) != 1 || res.Rows[0].FamilyNumber != "" {
		t.Errorf("rows = %+v", res.Rows)
	}
	wantErrors := []Issue{
		{Line: 4, Code: IssueInvalidEmail, Value: "pas-un-email"},
		{Line: 5, Code: IssueMissingParentName},
	}
	if len(res.Errors) != 2 || res.Errors[0] != wantErrors[0] || res.Errors[1] != wantErrors[1] {
		t.Errorf("errors = %+v", res.Errors)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != IssueDuplicateRow || res.Warnings[0].Line != 3 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestCut(t *testing.T) {
	if got := cut("abc", 3); got != "abc" {
		t.Errorf("cut = %q", got)
	}
	if got := cut("abcd", 3); got != "ab…" {
		t.Errorf("cut = %q", got)
	}
	if got := cut("éééé", 3); got != "éé…" || !utf8.ValidString(got) {
		t.Errorf("cut = %q", got)
	}
}

func TestDecodeText(t *testing.T) {
	if got, err := decodeText([]byte("\xEF\xBB\xBFabc")); err != nil || got != "abc" {
		t.Errorf("BOM: %q %v", got, err)
	}
	if got, err := decodeText([]byte("caf\xE9")); err != nil || got != "café" {
		t.Errorf("cp1252: %q %v", got, err)
	}
	if _, err := decodeText(bytes.Repeat([]byte{0}, 3)); !errors.Is(err, ErrUnreadable) {
		t.Errorf("NUL: %v", err)
	}
}
