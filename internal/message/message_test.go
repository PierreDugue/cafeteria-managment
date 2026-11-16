package message

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"cafeteria-management/internal/recipients"
)

func childRecipient(names ...string) recipients.Recipient {
	r := recipients.Recipient{ParentID: 7, ParentName: "Anne Martin", Email: "anne@example.com"}
	for i, n := range names {
		r.Children = append(r.Children, n)
		r.ChildIDs = append(r.ChildIDs, int64(i+1))
		r.FamilyNumbers = append(r.FamilyNumbers, "")
	}
	return r
}

func familyRecipient(entries ...[2]string) recipients.Recipient { // {number, name}
	r := recipients.Recipient{ParentID: 9, ParentName: "SILVA Ana", Email: "silva@example.com"}
	for i, e := range entries {
		r.FamilyNumbers = append(r.FamilyNumbers, e[0])
		r.Children = append(r.Children, e[1])
		r.ChildIDs = append(r.ChildIDs, int64(i+10))
	}
	return r
}

func TestPlaceholdersAndModeOf(t *testing.T) {
	tests := []struct {
		m    Mode
		want []string
	}{
		{ChildMode, []string{"parent_name"}},
		{FamilyMode, []string{"parent_name", "family_number"}},
		{AnyMode, []string{"parent_name", "family_number"}},
	}
	for _, tt := range tests {
		if got := Placeholders(tt.m); !slices.Equal(got, tt.want) {
			t.Errorf("Placeholders(%d) = %v, want %v", tt.m, got, tt.want)
		}
	}

	if got := ModeOf(nil); got != ChildMode {
		t.Errorf("ModeOf(nil) = %d", got)
	}
	if got := ModeOf([]recipients.Recipient{childRecipient("Léa")}); got != ChildMode {
		t.Errorf("children data: %d", got)
	}
	if got := ModeOf([]recipients.Recipient{childRecipient("Léa"), familyRecipient([2]string{"10", "DUPONT"})}); got != FamilyMode {
		t.Errorf("any family number: %d", got)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ name, subject, body, wantS, wantB string }{
		{"crlf and cr", "a\r\nb\rc", "x\r\ny\rz", "a\nb\nc", "x\ny\nz"},
		{"subject trimmed", "  Hello  ", "b", "Hello", "b"},
		{"body trailing removed, leading kept", "s", "  indented\n\n  line \t\n\n", "s", "  indented\n\n  line"},
		{"empty", "", "", "", ""},
		{"only spaces", "   ", " \n\t ", "", ""},
		{"accents kept", " Été ", "Éé  ", "Été", "Éé"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, b := Normalize(tt.subject, tt.body)
			if s != tt.wantS || b != tt.wantB {
				t.Fatalf("got %q, %q; want %q, %q", s, b, tt.wantS, tt.wantB)
			}
			s2, b2 := Normalize(s, b)
			if s2 != s || b2 != b {
				t.Fatalf("not idempotent: %q, %q", s2, b2)
			}
		})
	}
}

func TestCheckOK(t *testing.T) {
	tests := []struct {
		name, subject, body string
		m                   Mode
	}{
		{"plain text", "Hello", "Body", ChildMode},
		{"parent placeholder", "{{parent_name}} {{parent_name}}", "{{parent_name}}\n{{parent_name}}", ChildMode},
		{"family placeholders", "{{parent_name}} {{family_number}}", "{{parent_name}} {{family_number}}", FamilyMode},
		{"any mode accepts all", "{{parent_name}} {{family_number}}", "{{family_number}} {{parent_name}}", AnyMode},
		{"case and space tolerance", "{{ Parent_Name }}", "{{  PARENT_NAME}} {{parent_name }}", ChildMode},
		{"adjacent tokens", "{{parent_name}}{{parent_name}}", "{{parent_name}}{{parent_name}}", ChildMode},
		{"token at start and end", "x", "{{parent_name}} middle {{parent_name}}", ChildMode},
		{"single braces are text", "a { b } c", "{ } {x} } {", ChildMode},
		{"tab and newline in body", "s", "a\tb\nc", ChildMode},
		{"accents", "Été à l'école", "Chère famille — élève", ChildMode},
		{"subject 255 runes", strings.Repeat("é", 255), "b", ChildMode},
		{"body 20000 runes", "s", strings.Repeat("é", 20000), ChildMode},
		{"40 char token inside is checked not malformed", "s", "{{parent_name}}", ChildMode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Check(tt.subject, tt.body, tt.m); got != nil {
				t.Fatalf("problems = %+v", got)
			}
		})
	}
}

func TestCheckProblems(t *testing.T) {
	long40 := strings.Repeat("a", 40)
	long41 := strings.Repeat("a", 41)
	sub := func(k Kind, tok string) Problem { return Problem{Field: FieldSubject, Kind: k, Token: tok} }
	bod := func(k Kind, tok string) Problem { return Problem{Field: FieldBody, Kind: k, Token: tok} }

	tests := []struct {
		name, subject, body string
		m                   Mode
		want                []Problem
	}{
		{"unknown", "s", "{{parent_nam}}", ChildMode, []Problem{bod(KindUnknown, "{{parent_nam}}")}},
		{"unknown keeps written case and spaces", "s", "{{ Foo }}", AnyMode, []Problem{bod(KindUnknown, "{{ Foo }}")}},
		{"child_name in family mode is unknown", "s", "{{child_name}}", FamilyMode, []Problem{bod(KindUnknown, "{{child_name}}")}},
		{"children in family mode is unknown", "s", "{{children}}", FamilyMode, []Problem{bod(KindUnknown, "{{children}}")}},
		{"child_name in child mode is unknown", "s", "{{child_name}}", ChildMode, []Problem{bod(KindUnknown, "{{child_name}}")}},
		{"children in child mode is unknown", "s", "{{children}}", ChildMode, []Problem{bod(KindUnknown, "{{children}}")}},
		{"child_name in any mode is unknown", "s", "{{child_name}}", AnyMode, []Problem{bod(KindUnknown, "{{child_name}}")}},
		{"children in any mode is unknown", "s", "{{ Children }}", AnyMode, []Problem{bod(KindUnknown, "{{ Children }}")}},
		{"family_number in child mode", "s", "{{family_number}}", ChildMode, []Problem{bod(KindUnavailable, "{{family_number}}")}},
		{"children in subject child mode is unknown", "{{children}}", "b", ChildMode, []Problem{sub(KindUnknown, "{{children}}")}},
		{"child_name in subject any mode is unknown", "{{child_name}}", "b", AnyMode, []Problem{sub(KindUnknown, "{{child_name}}")}},
		{"children in subject family mode is unknown", "{{children}}", "b", FamilyMode, []Problem{sub(KindUnknown, "{{children}}")}},
		{"missing closing brace", "s", "{{parent_name}", ChildMode, []Problem{bod(KindMalformed, "{{parent_name}")}},
		{"stray closing", "s", "parent_name}}", ChildMode, []Problem{bod(KindMalformed, "}}")}},
		{"open alone", "s", "hello {{", ChildMode, []Problem{bod(KindMalformed, "{{")}},
		{"empty token", "s", "{{}}", ChildMode, []Problem{bod(KindMalformed, "{{}}")}},
		{"blank token", "s", "{{  }}", ChildMode, []Problem{bod(KindMalformed, "{{  }}")}},
		{"line break inside", "s", "{{a\nb}}", ChildMode, []Problem{bod(KindMalformed, "{{a"), bod(KindMalformed, "}}")}},
		{"brace inside", "s", "{{a{b}}", ChildMode, []Problem{bod(KindMalformed, "{{a"), bod(KindMalformed, "}}")}},
		{"41 chars inside", "s", "{{" + long41 + "}}", ChildMode, []Problem{bod(KindMalformed, "{{"+long41[:38]+"…")}},
		{"40 chars inside is unknown", "s", "{{" + long40 + "}}", ChildMode, []Problem{bod(KindUnknown, "{{"+long40[:38]+"…")}},
		{"empty subject and body", "", "", ChildMode, []Problem{sub(KindEmpty, ""), bod(KindEmpty, "")}},
		{"spaces only", "   ", " \n ", ChildMode, []Problem{sub(KindEmpty, ""), bod(KindEmpty, "")}},
		{"subject 256 runes", strings.Repeat("é", 256), "b", ChildMode, []Problem{sub(KindTooLong, "")}},
		{"body 20001 runes", "s", strings.Repeat("é", 20001), ChildMode, []Problem{bod(KindTooLong, "")}},
		{"too long reports only that problem", "s", strings.Repeat("{{x}}", 5000), ChildMode, []Problem{bod(KindTooLong, "")}},
		{"tab in subject", "a\tb", "b", ChildMode, []Problem{sub(KindInvalidChar, "")}},
		{"LF in subject", "a\nb", "b", ChildMode, []Problem{sub(KindInvalidChar, "")}},
		{"CR in subject becomes LF so invalid", "a\rb", "b", ChildMode, []Problem{sub(KindInvalidChar, "")}},
		{"NUL in subject", "a\x00b", "b", ChildMode, []Problem{sub(KindInvalidChar, "")}},
		{"NUL in body", "s", "a\x00b", ChildMode, []Problem{bod(KindInvalidChar, "")}},
		{"other control in body", "s", "a\x07b\u0085", ChildMode, []Problem{bod(KindInvalidChar, "")}},
		{"invalid utf8 subject", "a\xffb", "b", ChildMode, []Problem{sub(KindInvalidChar, "")}},
		{"invalid utf8 body", "s", "a\xc3(", ChildMode, []Problem{bod(KindInvalidChar, "")}},
		{"invalid utf8 stops token scan", "s", "{{nope}} \xff", ChildMode, []Problem{bod(KindInvalidChar, "")}},
		{"same problem twice reported once", "s", "{{nope}} {{nope}}", ChildMode, []Problem{bod(KindUnknown, "{{nope}}")}},
		{"different tokens both reported in order", "s", "{{b}} {{a}} {{b}}", ChildMode, []Problem{bod(KindUnknown, "{{b}}"), bod(KindUnknown, "{{a}}")}},
		{"subject before body", "{{x}}", "{{y}}", ChildMode, []Problem{sub(KindUnknown, "{{x}}"), bod(KindUnknown, "{{y}}")}},
		{"same token in both fields reported twice", "{{x}}", "{{x}}", ChildMode, []Problem{sub(KindUnknown, "{{x}}"), bod(KindUnknown, "{{x}}")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check(tt.subject, tt.body, tt.m)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// A token with a control character is echoed in the problem list, which the
// plan forbids ("never echo control characters").
func TestCheckDoesNotEchoControlCharacters(t *testing.T) {
	for _, p := range Check("s", "{{a\x01b}}", ChildMode) {
		if strings.ContainsFunc(p.Token, func(r rune) bool { return r < ' ' && r != '\n' }) {
			t.Errorf("problem %+v echoes a control character", p)
		}
	}
}

func TestCheckNormalizesItsInput(t *testing.T) {
	// CRLF and surrounding whitespace are not problems.
	if got := Check("  s  ", "line1\r\nline2\r\n\r\n", ChildMode); got != nil {
		t.Fatalf("problems = %+v", got)
	}
	// A trailing line break on the subject is not trimmed (only spaces are).
	if got := Check("s\r\n", "b", ChildMode); len(got) != 1 || got[0].Kind != KindInvalidChar {
		t.Fatalf("problems = %+v", got)
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		items []string
		and   string
		want  string
	}{
		{nil, "et", ""},
		{[]string{"Léa"}, "et", "Léa"},
		{[]string{"Léa", "Tom"}, "et", "Léa et Tom"},
		{[]string{"Léa", "Tom", "Zoé"}, "et", "Léa, Tom et Zoé"},
		{[]string{"Léa", "Tom", "Zoé", "Éva"}, "et", "Léa, Tom, Zoé et Éva"},
		{[]string{"Léa"}, "and", "Léa"},
		{[]string{"Léa", "Tom"}, "and", "Léa and Tom"},
		{[]string{"Léa", "Tom", "Zoé"}, "and", "Léa, Tom and Zoé"},
		{[]string{"A", "B", "C", "D"}, "and", "A, B, C and D"},
	}
	for _, tt := range tests {
		if got := join(tt.items, tt.and); got != tt.want {
			t.Errorf("join(%v, %q) = %q, want %q", tt.items, tt.and, got, tt.want)
		}
	}
}

func render(t *testing.T, subject, body string, r recipients.Recipient, m Mode, and string) Email {
	t.Helper()
	e, err := Render(subject, body, r, m, and)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return e
}

func TestRenderChildMode(t *testing.T) {
	r := childRecipient("Léa", "Tom", "Zoé")
	e := render(t, "Pour {{parent_name}}", "Bonjour {{ Parent_Name }},\nMerci", r, ChildMode, "et")
	if e.Subject != "Pour Anne Martin" {
		t.Errorf("subject = %q", e.Subject)
	}
	if want := "Bonjour Anne Martin,\nMerci"; e.Body != want {
		t.Errorf("body = %q, want %q", e.Body, want)
	}
	if e.ParentID != 7 || e.To != "anne@example.com" || e.ToName != "Anne Martin" {
		t.Errorf("email = %+v", e)
	}
	if !slices.Equal(e.ChildIDs, []int64{1, 2, 3}) || !slices.Equal(e.ChildNames, []string{"Léa", "Tom", "Zoé"}) {
		t.Errorf("children = %v %v", e.ChildIDs, e.ChildNames)
	}

	t.Run("text without token is unchanged and body keeps newlines", func(t *testing.T) {
		e := render(t, "Hello { }", "a\r\n\r\n  b }\tc\r\n", r, ChildMode, "et")
		if e.Subject != "Hello { }" || e.Body != "a\n\n  b }\tc" {
			t.Errorf("email = %q / %q", e.Subject, e.Body)
		}
	})
}

func TestRenderFamilyMode(t *testing.T) {
	t.Run("silva both selected", func(t *testing.T) {
		r := familyRecipient([2]string{"10", "SILVA Ana"}, [2]string{"20", "SILVA Rui"})
		e := render(t, "{{family_number}}", "Dear {{parent_name}}", r, FamilyMode, "et")
		if e.Subject != "10 et 20" || e.Body != "Dear SILVA Ana et SILVA Rui" {
			t.Errorf("email = %+v", e)
		}
		if e.ToName != "SILVA Ana et SILVA Rui" {
			t.Errorf("ToName = %q", e.ToName)
		}
		if !slices.Equal(e.ChildNames, []string{"SILVA Ana", "SILVA Rui"}) || !slices.Equal(e.ChildIDs, []int64{10, 11}) {
			t.Errorf("children = %v %v", e.ChildNames, e.ChildIDs)
		}
	})
	t.Run("silva only the second selected uses its own name", func(t *testing.T) {
		r := familyRecipient([2]string{"20", "SILVA Rui"}) // ParentName is "SILVA Ana"
		e := render(t, "s", "{{parent_name}} {{family_number}}", r, FamilyMode, "et")
		if e.Body != "SILVA Rui 20" || e.ToName != "SILVA Rui" {
			t.Errorf("email = %+v", e)
		}
	})
	t.Run("same name other case and spaces once", func(t *testing.T) {
		r := familyRecipient([2]string{"10", "SILVA Ana"}, [2]string{"20", "silva  ana"}, [2]string{"30", "Silva Ana"})
		e := render(t, "s", "{{parent_name}}", r, FamilyMode, "et")
		if e.Body != "SILVA Ana" {
			t.Errorf("body = %q", e.Body)
		}
	})
	t.Run("accents ignored when deduplicating", func(t *testing.T) {
		r := familyRecipient([2]string{"10", "ÉLISE"}, [2]string{"20", "Elise"})
		if e := render(t, "s", "{{parent_name}}", r, FamilyMode, "et"); e.Body != "ÉLISE" {
			t.Errorf("body = %q", e.Body)
		}
	})
	t.Run("family numbers deduplicated by exact value", func(t *testing.T) {
		r := familyRecipient([2]string{"10", "A"}, [2]string{"10", "B"}, [2]string{"010", "C"}, [2]string{"20", "D"})
		e := render(t, "s", "{{family_number}}", r, FamilyMode, "and")
		if e.Body != "10, 010 and 20" {
			t.Errorf("body = %q", e.Body)
		}
	})
	t.Run("english", func(t *testing.T) {
		r := familyRecipient([2]string{"1234", "SILVA Ana"}, [2]string{"1250", "SILVA Rui"})
		if e := render(t, "s", "{{parent_name}} / {{family_number}}", r, FamilyMode, "and"); e.Body != "SILVA Ana and SILVA Rui / 1234 and 1250" {
			t.Errorf("body = %q", e.Body)
		}
	})
}

func TestRenderValuesAreNotExpandedAgain(t *testing.T) {
	r := childRecipient("{{parent_name}}", "}}", "{{children}}")
	r.ParentName = "{{family_number}} }} {{parent_name}}"
	e := render(t, "{{parent_name}}|{{parent_name}}", "{{parent_name}}", r, ChildMode, "et")
	if want := "{{family_number}} }} {{parent_name}}|{{family_number}} }} {{parent_name}}"; e.Subject != want {
		t.Errorf("subject = %q, want %q", e.Subject, want)
	}
	if want := "{{family_number}} }} {{parent_name}}"; e.Body != want {
		t.Errorf("body = %q, want %q", e.Body, want)
	}

	f := familyRecipient([2]string{"{{x}}", "{{parent_name}}"})
	e = render(t, "s", "{{parent_name}} {{family_number}}", f, FamilyMode, "et")
	if e.Body != "{{parent_name}} {{x}}" {
		t.Errorf("family body = %q", e.Body)
	}
}

func TestRenderHeaderSafe(t *testing.T) {
	r := childRecipient("Léa")
	r.ParentName = "Anne\r\nBcc: evil@example.com\x00  Martin"
	e := render(t, "Pour {{parent_name}}", "{{parent_name}}", r, ChildMode, "et")
	for _, s := range []string{e.Subject, e.ToName} {
		if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' }) {
			t.Errorf("control character in header value %q", s)
		}
		if strings.Contains(s, "  ") {
			t.Errorf("spaces not collapsed in %q", s)
		}
	}
	if e.ToName != "Anne Bcc: evil@example.com Martin" {
		t.Errorf("ToName = %q", e.ToName)
	}
	if e.Subject != "Pour Anne Bcc: evil@example.com Martin" {
		t.Errorf("Subject = %q", e.Subject)
	}
	// The body is not header text: its values are kept as they are.
	if e.Body != r.ParentName || !strings.Contains(e.Body, "\r\n") {
		t.Errorf("body should keep value characters: %q", e.Body)
	}
}

func TestRenderErrors(t *testing.T) {
	r := childRecipient("Léa")
	for name, in := range map[string][2]string{
		"unknown":       {"s", "{{nope}}"},
		"empty subject": {"", "b"},
		"unavailable":   {"s", "{{family_number}}"},
		"child_name":    {"s", "{{child_name}}"},
		"children":      {"{{children}}", "b"},
		"malformed":     {"s", "{{parent_name}"},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := Render(in[0], in[1], r, ChildMode, "et")
			if !errors.Is(err, ErrProblems) {
				t.Fatalf("err = %v, want ErrProblems", err)
			}
			if !reflect.DeepEqual(e, Email{}) {
				t.Errorf("email = %+v, want zero", e)
			}
		})
	}

	t.Run("any mode is an error", func(t *testing.T) {
		if _, err := Render("s", "b", r, AnyMode, "et"); err == nil || errors.Is(err, ErrProblems) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("family placeholder in child mode", func(t *testing.T) {
		if _, err := Render("s", "{{child_name}}", familyRecipient([2]string{"1", "A"}), FamilyMode, "et"); !errors.Is(err, ErrProblems) {
			t.Fatalf("err = %v", err)
		}
		if _, err := Render("s", "{{children}}", familyRecipient([2]string{"1", "A"}), FamilyMode, "et"); !errors.Is(err, ErrProblems) {
			t.Fatalf("children: err = %v", err)
		}
	})
}

func TestRenderDoesNotShareSlices(t *testing.T) {
	r := childRecipient("Léa", "Tom")
	e := render(t, "s", "b", r, ChildMode, "et")
	e.ChildIDs[0], e.ChildNames[0] = 99, "X"
	if r.ChildIDs[0] == 99 || r.Children[0] == "X" {
		t.Error("Email shares slices with the recipient")
	}
}

func TestRenderToNameWithoutPlaceholderInMessage(t *testing.T) {
	e := render(t, "s", "b", childRecipient("Léa"), ChildMode, "et")
	if e.ToName != "Anne Martin" || e.Body != "b" {
		t.Errorf("email = %+v", e)
	}
}

func TestProblemTokensNeverEchoControlCharacters(t *testing.T) {
	long := strings.Repeat("é", 10) + "\x00" + strings.Repeat("z", 40) // 51 runes
	tests := []struct {
		name, text string
		mode       Mode
	}{
		{"unclosed with NUL", "{{a\x00", AnyMode},
		{"tab inside token", "{{a\tb}}", AnyMode},
		{"stray close after control chars", "x\x01\x02}}", AnyMode},
		{"unavailable token with control char", "{{family_number\x00}}", ChildMode},
		{"unknown token with DEL and C1", "{{a\x7f\u0085b}}", AnyMode},
		{"long token with control char at the cut", "{{" + long + "}}", AnyMode},
		{"unclosed long with control char", "{{" + long, AnyMode},
		{"typed replacement char", "{{a�b}}", AnyMode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The body allows tab and newline, so use the body field.
			ps := Check("s", tt.text, tt.mode)
			sawToken := false
			for _, p := range ps {
				for _, r := range p.Token {
					if unicode.IsControl(r) || r == utf8.RuneError {
						t.Errorf("problem %+v echoes %U", p, r)
					}
				}
				if p.Token != "" {
					sawToken = true
					if n := utf8.RuneCountInString(strings.TrimSuffix(p.Token, "…")); n > 40 {
						t.Errorf("token %q is %d runes", p.Token, n)
					}
				}
			}
			if !sawToken {
				t.Errorf("expected a problem with a token, got %+v", ps)
			}
		})
	}

	t.Run("cut still 40 runes plus ellipsis after mapping", func(t *testing.T) {
		ps := Check("s", "{{"+long+"}}", AnyMode)
		if len(ps) == 0 {
			t.Fatal("no problem")
		}
		tok := ps[len(ps)-1].Token
		if !strings.HasSuffix(tok, "…") || utf8.RuneCountInString(tok) != 41 || !strings.Contains(tok, "?") {
			t.Errorf("token = %q (%d runes)", tok, utf8.RuneCountInString(tok))
		}
	})

	t.Run("invalid UTF-8 gives invalid_char without any token", func(t *testing.T) {
		ps := Check("s", "{{a\xff\xfeb}}", AnyMode)
		if len(ps) != 1 || ps[0].Kind != KindInvalidChar || ps[0].Token != "" {
			t.Errorf("problems = %+v", ps)
		}
	})
}

// ---- RenderAll (plan 005) -------------------------------------------------

func namedRecipient(id int64, name, email string) recipients.Recipient {
	return recipients.Recipient{ParentID: id, ParentName: name, Email: email,
		Children: []string{"Léa"}, ChildIDs: []int64{id * 10}, FamilyNumbers: []string{""}}
}

func TestRenderAllMatchesRenderOneByOne(t *testing.T) {
	for _, tt := range []struct {
		name string
		rs   []recipients.Recipient
		m    Mode
	}{
		{"children", []recipients.Recipient{childRecipient("Léa", "Tom"), namedRecipient(2, "Éloïse Durand", "e@example.com")}, ChildMode},
		{"families", []recipients.Recipient{
			familyRecipient([2]string{"1234", "SILVA Ana"}, [2]string{"1250", "SILVA Rui"}),
			familyRecipient([2]string{"10", "DUPONT"}),
		}, FamilyMode},
	} {
		t.Run(tt.name, func(t *testing.T) {
			subject, body := "Pour {{parent_name}}", "Bonjour {{parent_name}}"
			if tt.m == FamilyMode {
				subject, body = "Famille {{family_number}}", "Cher {{parent_name}} ({{family_number}})"
			}
			got, problems, err := RenderAll(subject, body, tt.rs, tt.m, "et")
			if err != nil || problems != nil {
				t.Fatalf("err = %v, problems = %+v", err, problems)
			}
			if len(got) != len(tt.rs) {
				t.Fatalf("emails = %d, want %d", len(got), len(tt.rs))
			}
			for i, r := range tt.rs {
				want, err := Render(subject, body, r, tt.m, "et")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got[i], want) {
					t.Errorf("email %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestRenderAllFamilyNumbersCopied(t *testing.T) {
	r := familyRecipient([2]string{"10", "A"}, [2]string{"20", "B"})
	got, _, err := RenderAll("s", "b", []recipients.Recipient{r}, FamilyMode, "et")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[0].FamilyNumbers, []string{"10", "20"}) {
		t.Fatalf("FamilyNumbers = %v", got[0].FamilyNumbers)
	}
	got[0].FamilyNumbers[0] = "x"
	if r.FamilyNumbers[0] != "10" {
		t.Error("FamilyNumbers shares memory with the recipient")
	}
}

func TestRenderAllProblemsGiveNoEmails(t *testing.T) {
	rs := []recipients.Recipient{childRecipient("Léa")}
	emails, problems, err := RenderAll("s", "{{parent_nam}}", rs, ChildMode, "et")
	if err != nil || emails != nil || len(problems) != 1 || problems[0].Kind != KindUnknown {
		t.Fatalf("emails = %v, problems = %+v, err = %v", emails, problems, err)
	}
	emails, problems, err = RenderAll("", "b", rs, ChildMode, "et")
	if err != nil || emails != nil || len(problems) != 1 || problems[0].Kind != KindEmpty {
		t.Fatalf("emails = %v, problems = %+v, err = %v", emails, problems, err)
	}
}

func TestRenderAllBadMode(t *testing.T) {
	for _, m := range []Mode{AnyMode, Mode(99)} {
		if _, _, err := RenderAll("s", "b", []recipients.Recipient{childRecipient("Léa")}, m, "et"); err == nil {
			t.Errorf("mode %v: want an error", m)
		}
	}
}

func TestRenderAllEmptyRecipients(t *testing.T) {
	emails, problems, err := RenderAll("s", "b", nil, ChildMode, "et")
	if err != nil || problems != nil || len(emails) != 0 {
		t.Fatalf("emails = %v, problems = %v, err = %v", emails, problems, err)
	}
}

func TestRenderAllSubjectLimit(t *testing.T) {
	sub := "{{parent_name}}"
	t.Run("exactly 255 runes is accepted (runes, not bytes)", func(t *testing.T) {
		rs := []recipients.Recipient{namedRecipient(1, strings.Repeat("é", 255), "a@example.com")}
		emails, problems, err := RenderAll(sub, "b", rs, ChildMode, "et")
		if err != nil || problems != nil || utf8.RuneCountInString(emails[0].Subject) != 255 {
			t.Fatalf("problems = %+v, err = %v", problems, err)
		}
		if len(emails[0].Subject) <= 255 {
			t.Fatal("test must use multi-byte runes")
		}
	})
	t.Run("256 runes is one problem with the first address and the count", func(t *testing.T) {
		rs := []recipients.Recipient{
			namedRecipient(1, "Court", "ok@example.com"),
			namedRecipient(2, strings.Repeat("é", 256), "first@example.com"),
			namedRecipient(3, strings.Repeat("é", 300), "second@example.com"),
		}
		emails, problems, err := RenderAll(sub, "b", rs, ChildMode, "et")
		if err != nil || emails != nil {
			t.Fatalf("emails = %v, err = %v", emails, err)
		}
		want := []Problem{{Field: FieldSubject, Kind: KindRenderedTooLong, Recipient: "first@example.com", Count: 2}}
		if !reflect.DeepEqual(problems, want) {
			t.Fatalf("problems = %+v, want %+v", problems, want)
		}
	})
	t.Run("counted after headerSafe", func(t *testing.T) {
		// 254 letters + 10 spaces collapse to 255 runes with one more space.
		name := strings.Repeat("a", 127) + strings.Repeat(" ", 10) + strings.Repeat("b", 127)
		rs := []recipients.Recipient{namedRecipient(1, name, "a@example.com")}
		emails, problems, err := RenderAll(sub, "b", rs, ChildMode, "et")
		if err != nil || problems != nil {
			t.Fatalf("problems = %+v, err = %v", problems, err)
		}
		if n := utf8.RuneCountInString(emails[0].Subject); n != 255 {
			t.Fatalf("runes = %d", n)
		}
	})
	t.Run("typed subject at the cap still fails when the value grows it", func(t *testing.T) {
		long := strings.Repeat("x", 240) + "{{parent_name}}" // 255 runes typed
		rs := []recipients.Recipient{namedRecipient(1, strings.Repeat("é", 20), "a@example.com")}
		_, problems, err := RenderAll(long, "b", rs, ChildMode, "et")
		if err != nil || len(problems) != 1 || problems[0].Kind != KindRenderedTooLong || problems[0].Count != 1 {
			t.Fatalf("problems = %+v, err = %v", problems, err)
		}
	})
}
