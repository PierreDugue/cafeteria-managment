package importer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const famHeader = "NUMFAM;NOMFACT;EMAIL PAYEUR\n"

func famRows(t *testing.T, lines ...string) *Result {
	t.Helper()
	return parseCSVString(t, famHeader+strings.Join(lines, "\n")+"\n")
}

func TestFamilySameEmailOneEntry(t *testing.T) {
	res := famRows(t,
		"10;DUPONT Marie;Marie@Example.fr",
		"10;DUPONT Marie;marie@example.FR",
		"10;dupont marie;MARIE@example.fr",
	)
	if len(res.Rows) != 1 || len(res.Warnings) != 0 || len(res.Errors) != 0 {
		t.Fatalf("rows=%+v warnings=%+v errors=%+v", res.Rows, res.Warnings, res.Errors)
	}
	if r := res.Rows[0]; r.Line != 2 || r.Email != "marie@example.fr" || r.FamilyNumber != "10" || r.ChildName != "DUPONT Marie" {
		t.Errorf("row = %+v", r)
	}
	if res.RowsRead != 3 || res.Layout != LayoutFamilies {
		t.Errorf("read=%d layout=%v", res.RowsRead, res.Layout)
	}
}

func TestFamilyTwoPayers(t *testing.T) {
	res := famRows(t, "10;DUPONT;a@example.fr", "10;DUPONT;b@example.fr", "10;DUPONT;a@example.fr")
	if len(res.Rows) != 2 || res.Rows[0].Email != "a@example.fr" || res.Rows[1].Email != "b@example.fr" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestFamilyBillingNameConflict(t *testing.T) {
	res := famRows(t,
		"10;DUPONT Marie;a@example.fr",
		"10;DUPONT MARIE;a@example.fr", // same name, other case: no warning
		"10;Dupônt Marié;a@example.fr", // accents: no warning
		"10;MARTIN Paul;a@example.fr",  // conflict on line 5
	)
	if len(res.Rows) != 1 || res.Rows[0].ChildName != "DUPONT Marie" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := Issue{Line: 5, Code: IssueBillingNameConflict, Value: "MARTIN Paul"}
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

func TestFamilySharedEmailOtherNames(t *testing.T) {
	res := famRows(t, "10;DUPONT;shared@example.fr", "11;MARTIN;shared@example.fr")
	if len(res.Rows) != 2 || res.Rows[0].ChildName != "DUPONT" || res.Rows[1].ChildName != "MARTIN" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := Issue{Line: 3, Code: IssueParentNameConflict, Value: "MARTIN"}
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %+v", res.Errors)
	}
}

func TestFamilyEmptyEmailOnly(t *testing.T) {
	res := famRows(t, "10;DUPONT;", "10;DUPONT;  ")
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := Row{Line: 2, ParentName: "DUPONT", ChildName: "DUPONT", FamilyNumber: "10"}
	if res.Rows[0] != want {
		t.Errorf("row = %+v, want %+v", res.Rows[0], want)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %+v (file must be importable)", res.Errors)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	for i, w := range res.Warnings {
		if w.Code != IssueMissingPayerEmail || w.Value != "10" || w.Line != i+2 {
			t.Errorf("warning %d = %+v", i, w)
		}
	}
}

func TestFamilyInvalidEmail(t *testing.T) {
	for _, bad := range []string{"jean@gmailcom", "-", "néant", "Jean <j@example.fr>", "a@b"} {
		t.Run(bad, func(t *testing.T) {
			res := famRows(t, "10;DUPONT;"+bad)
			if len(res.Rows) != 1 || len(res.Errors) != 0 {
				t.Fatalf("rows=%+v errors=%+v", res.Rows, res.Errors)
			}
			wantVal := strings.ToLower(bad)
			r := res.Rows[0]
			if r.Email != "" || r.InvalidEmail != wantVal || r.FamilyNumber != "10" || r.Line != 2 {
				t.Errorf("row = %+v", r)
			}
			want := Issue{Line: 2, Code: IssueInvalidPayerEmail, Value: wantVal}
			if len(res.Warnings) != 1 || res.Warnings[0] != want {
				t.Errorf("warnings = %+v", res.Warnings)
			}
		})
	}
}

func TestFamilyMailtoPrefixAccepted(t *testing.T) {
	res := famRows(t, "10;DUPONT;mailto:A@Example.fr")
	if len(res.Rows) != 1 || res.Rows[0].Email != "a@example.fr" || len(res.Warnings) != 0 {
		t.Fatalf("rows=%+v warnings=%+v", res.Rows, res.Warnings)
	}
}

func TestFamilyValidPlusEmptyLine(t *testing.T) {
	res := famRows(t, "10;DUPONT;a@example.fr", "10;DUPONT;")
	if len(res.Rows) != 1 || res.Rows[0].Email != "a@example.fr" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := Issue{Line: 3, Code: IssueMissingPayerEmail, Value: "10"}
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

func TestFamilyValidPlusInvalidLine(t *testing.T) {
	res := famRows(t, "10;DUPONT;a@example.fr", "10;DUPONT;oups@gmailcom")
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.Rows[0].Email != "a@example.fr" || res.Rows[0].InvalidEmail != "" {
		t.Errorf("valid row = %+v", res.Rows[0])
	}
	if r := res.Rows[1]; r.Email != "" || r.InvalidEmail != "oups@gmailcom" || r.Line != 3 || r.FamilyNumber != "10" {
		t.Errorf("no-email row = %+v", r)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Line != 3 || res.Warnings[0].Code != IssueInvalidPayerEmail {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestFamilyTwoInvalidValues(t *testing.T) {
	res := famRows(t, "10;DUPONT;first@gmailcom", "10;DUPONT;second@gmailcom")
	if len(res.Rows) != 1 || res.Rows[0].InvalidEmail != "first@gmailcom" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 2 || res.Warnings[0].Value != "first@gmailcom" || res.Warnings[1].Value != "second@gmailcom" {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestFamilyEmptyThenInvalid(t *testing.T) {
	res := famRows(t, "10;EMPTY NAME;", "10;INVALID NAME;oups")
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	r := res.Rows[0]
	if r.Line != 3 || r.ChildName != "INVALID NAME" || r.InvalidEmail != "oups" || r.Email != "" {
		t.Errorf("row = %+v", r)
	}
}

func TestFamilyInvalidValueCut(t *testing.T) {
	long := strings.Repeat("é", 300)
	res := famRows(t, "10;DUPONT;"+long)
	r := res.Rows[0]
	if !utf8.ValidString(r.InvalidEmail) || !strings.HasSuffix(r.InvalidEmail, "…") {
		t.Fatalf("InvalidEmail = %q", r.InvalidEmail)
	}
	if n := utf8.RuneCountInString(r.InvalidEmail); n != maxInvalidEmailLen {
		t.Errorf("runes = %d, want %d", n, maxInvalidEmailLen)
	}
	if res.Warnings[0].Value != r.InvalidEmail {
		t.Errorf("warning value differs from stored value")
	}

	exact := strings.Repeat("x", maxInvalidEmailLen)
	res = famRows(t, "10;DUPONT;"+exact)
	if res.Rows[0].InvalidEmail != exact {
		t.Errorf("a value of exactly the limit must not be cut")
	}
}

func TestFamilyBlockingIssues(t *testing.T) {
	res := famRows(t,
		";DUPONT;oups",           // 2: missing NUMFAM, no email warning
		"11;;",                   // 3: missing NOMFACT, no email warning
		";;a@example.fr",         // 4: both missing
		"12;MARTIN;a@example.fr", // 5: fine
	)
	want := []Issue{
		{Line: 2, Code: IssueMissingFamilyNumber},
		{Line: 3, Code: IssueMissingBillingName},
		{Line: 4, Code: IssueMissingFamilyNumber},
		{Line: 4, Code: IssueMissingBillingName},
	}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %+v", res.Errors)
	}
	for i := range want {
		if res.Errors[i] != want[i] {
			t.Errorf("error %d = %+v, want %+v", i, res.Errors[i], want[i])
		}
	}
	if len(res.Rows) != 1 || res.Rows[0].Line != 5 {
		t.Errorf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	if res.RowsRead != 4 {
		t.Errorf("RowsRead = %d", res.RowsRead)
	}
}

func TestFamilyRowsOrderedByLine(t *testing.T) {
	res := famRows(t,
		"10;A;a@example.fr",      // 2
		"20;B;b@example.fr",      // 3
		"10;A;second@example.fr", // 4
		"20;B;",                  // 5: warning only
		"10;A;bad",               // 6: no-email entry of family 10
	)
	var lines []int
	for _, r := range res.Rows {
		lines = append(lines, r.Line)
	}
	want := []int{2, 3, 4, 6}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
	for i := 1; i < len(res.Warnings); i++ {
		if res.Warnings[i-1].Line > res.Warnings[i].Line {
			t.Errorf("warnings not ordered: %+v", res.Warnings)
		}
	}
}

func TestFamilyNumberIsExactString(t *testing.T) {
	// "10" and "010" are different families; surrounding spaces are cleaned.
	res := famRows(t, " 10 ;A;a@example.fr", "010;A;a@example.fr", "10;A;a@example.fr")
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestFamilyLayoutFromXLSX(t *testing.T) {
	buf := xlsx(t, [][]any{
		{"NUMFAM", "NOMFACT", "EMAIL PAYEUR"},
		{"10", "DUPONT", "a@example.fr"},
		{"10", "DUPONT", "A@example.fr"},
		{"11", "MARTIN", ""},
	})
	res, err := ParseXLSX(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Families() || res.Sheet == "" {
		t.Fatalf("layout=%v sheet=%q", res.Layout, res.Sheet)
	}
	if len(res.Rows) != 2 || res.Rows[1].Email != "" || res.Rows[1].FamilyNumber != "11" {
		t.Errorf("rows = %+v", res.Rows)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != IssueMissingPayerEmail || res.Warnings[0].Line != 4 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestExcelLayoutStillBlocksBadEmail(t *testing.T) {
	res, err := ParseXLSX(xlsx(t, [][]any{
		{"nom parent", "nom enfant", "email"},
		{"Jean", "Léa", ""},
		{"Jean", "Tom", "jean@gmailcom"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Layout != LayoutChildren || len(res.Errors) != 2 || len(res.Rows) != 0 {
		t.Fatalf("layout=%v errors=%+v rows=%+v", res.Layout, res.Errors, res.Rows)
	}
	if res.Errors[0].Code != IssueMissingEmail || res.Errors[1].Code != IssueInvalidEmail {
		t.Errorf("errors = %+v", res.Errors)
	}
}

func TestParseXLSXMissingFamilyColumns(t *testing.T) {
	_, err := ParseXLSX(xlsx(t, [][]any{{"NUMFAM", "NOMFACT"}, {"1", "A"}}))
	var mc *MissingColumnsError
	if err == nil || !asMissing(err, &mc) || mc.Columns[0] != ColPayerEmail {
		t.Fatalf("err = %v", err)
	}
}

func asMissing(err error, target **MissingColumnsError) bool {
	m, ok := err.(*MissingColumnsError)
	if ok {
		*target = m
	}
	return ok
}
