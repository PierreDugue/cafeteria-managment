// Package message checks and renders the plain-text email of one recipient
// from a subject and a body with placeholders (SPEC §4.3).
//
// Placeholders are read by a small hand-written scanner, never by
// text/template: the text is typed by the user, and values (names from the
// imported file) are appended to the output without being read again, so
// data can never act as template code.
package message

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"cafeteria-management/internal/recipients"
	"cafeteria-management/internal/textnorm"
)

// Mode tells which placeholders are available.
type Mode int

// Modes. A request is in FamilyMode when the data are family entries (CSV
// export, SPEC §4.1.1) and in ChildMode otherwise (Excel).
const (
	AnyMode    Mode = iota // template save: every known placeholder accepted
	ChildMode              // Excel children data
	FamilyMode             // CSV family data
)

// Placeholder names, written {{name}} in a message.
const (
	ParentName   = "parent_name"
	FamilyNumber = "family_number"
)

// Limits, in runes.
const (
	MaxSubjectLen = 255
	MaxBodyLen    = 20000
)

// maxTokenLen is the longest text allowed between {{ and }}, in runes; it is
// also where a token shown in a problem is cut.
const maxTokenLen = 40

// Placeholders returns the placeholder names usable in m, in display order.
func Placeholders(m Mode) []string {
	switch m {
	case ChildMode:
		return []string{ParentName}
	case FamilyMode, AnyMode:
		return []string{ParentName, FamilyNumber}
	default:
		return nil
	}
}

// ModeOf returns FamilyMode if any entry has a family number, else ChildMode.
func ModeOf(rs []recipients.Recipient) Mode {
	for _, r := range rs {
		for _, n := range r.FamilyNumbers {
			if n != "" {
				return FamilyMode
			}
		}
	}
	return ChildMode
}

// Field is the part of the message a problem is about.
type Field string

// Fields.
const (
	FieldSubject Field = "subject"
	FieldBody    Field = "body"
)

// Kind is the type of a problem.
type Kind string

// Kinds of problems.
const (
	KindUnknown     Kind = "unknown"
	KindUnavailable Kind = "unavailable"
	KindMalformed   Kind = "malformed"
	KindEmpty       Kind = "empty"
	KindTooLong     Kind = "too_long"
	KindInvalidChar Kind = "invalid_char"
)

// Problem is one thing to fix before the message can be used.
type Problem struct {
	Field Field
	Kind  Kind
	Token string // as written, cut to 40 runes; "" for empty, too_long, invalid_char
}

// Normalize returns subject and body with "\n" line endings, the subject
// trimmed of spaces and trailing whitespace removed from the end of the
// body. It is idempotent.
func Normalize(subject, body string) (string, string) {
	subject = strings.Trim(unifyNewlines(subject), " ")
	body = strings.TrimRightFunc(unifyNewlines(body), unicode.IsSpace)
	return subject, body
}

func unifyNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Check normalizes subject and body and returns every problem for mode m,
// subject problems first, or nil.
func Check(subject, body string, m Mode) []Problem {
	subject, body = Normalize(subject, body)
	var out []Problem
	out = append(out, checkField(FieldSubject, subject, m)...)
	out = append(out, checkField(FieldBody, body, m)...)
	return out
}

func checkField(f Field, text string, m Mode) []Problem {
	maxLen := MaxBodyLen
	if f == FieldSubject {
		maxLen = MaxSubjectLen
	}
	switch {
	case text == "":
		return []Problem{{Field: f, Kind: KindEmpty}}
	case !utf8.ValidString(text):
		return []Problem{{Field: f, Kind: KindInvalidChar}}
	case utf8.RuneCountInString(text) > maxLen:
		return []Problem{{Field: f, Kind: KindTooLong}}
	}

	var out []Problem
	seen := map[Problem]bool{}
	add := func(p Problem) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if hasForbiddenChar(f, text) {
		add(Problem{Field: f, Kind: KindInvalidChar})
	}
	for _, seg := range scan(text) {
		switch {
		case seg.malformed:
			add(Problem{Field: f, Kind: KindMalformed, Token: cutToken(seg.raw)})
		case seg.isToken:
			if k, bad := tokenProblem(seg.name, m); bad {
				add(Problem{Field: f, Kind: k, Token: cutToken(seg.raw)})
			}
		}
	}
	return out
}

func hasForbiddenChar(f Field, text string) bool {
	for _, r := range text {
		if !unicode.IsControl(r) {
			continue
		}
		if f == FieldBody && (r == '\n' || r == '\t') {
			continue
		}
		return true
	}
	return false
}

func tokenProblem(name string, m Mode) (Kind, bool) {
	switch name {
	case ParentName, FamilyNumber:
	default:
		return KindUnknown, true
	}
	if m != AnyMode {
		for _, p := range Placeholders(m) {
			if p == name {
				return "", false
			}
		}
		return KindUnavailable, true
	}
	return "", false
}

// cutToken limits a token echoed in a problem to 40 runes.
// Control characters and invalid UTF-8 are replaced by "?" first.
func cutToken(s string) string {
	r := []rune(s)
	for i, c := range r {
		if unicode.IsControl(c) || c == utf8.RuneError {
			r[i] = '?'
		}
	}
	if len(r) <= maxTokenLen {
		return string(r)
	}
	return string(r[:maxTokenLen]) + "…"
}

// segment is a piece of text read by scan: literal text, a placeholder or a
// badly written placeholder.
type segment struct {
	text      string // literal text
	raw       string // token or malformed text as written
	name      string // token name: trimmed, lower-case
	isToken   bool
	malformed bool
}

// scan reads text once, left to right. A token is "{{", 1 to 40 characters
// other than "{", "}" and a line break, and "}}". A "{{" without a valid
// token, and a "}}" outside a token, are malformed. Single braces are text.
func scan(text string) []segment {
	var out []segment
	lit := 0 // start of the pending literal text
	flush := func(end int) {
		if end > lit {
			out = append(out, segment{text: text[lit:end]})
		}
	}
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "{{"):
			flush(i)
			end, seg := readToken(text, i)
			out = append(out, seg)
			i, lit = end, end
		case strings.HasPrefix(text[i:], "}}"):
			flush(i)
			out = append(out, segment{raw: "}}", malformed: true})
			i += 2
			lit = i
		default:
			i++
		}
	}
	flush(len(text))
	return out
}

// readToken reads the token starting at text[start:] ("{{") and returns the
// index after what it consumed.
func readToken(text string, start int) (int, segment) {
	j := start + 2
	for j < len(text) {
		switch text[j] {
		case '}':
			if !strings.HasPrefix(text[j:], "}}") {
				// "{{name}" : consume the lone brace with the token.
				return j + 1, segment{raw: text[start : j+1], malformed: true}
			}
			inner := text[start+2 : j]
			trimmed := strings.TrimSpace(inner)
			raw := text[start : j+2]
			if trimmed == "" || utf8.RuneCountInString(inner) > maxTokenLen {
				return j + 2, segment{raw: raw, malformed: true}
			}
			return j + 2, segment{raw: raw, name: strings.ToLower(trimmed), isToken: true}
		case '{', '\n':
			return j, segment{raw: text[start:j], malformed: true}
		}
		j++
	}
	return j, segment{raw: text[start:], malformed: true}
}

// Email is one rendered email, ready for the sender (SPEC §4.4, §5.3).
type Email struct {
	ParentID   int64
	ToName     string // value of {{parent_name}}, header-safe
	To         string
	Subject    string // one line, header-safe
	Body       string // plain text, "\n" line endings
	ChildIDs   []int64
	ChildNames []string // family mode: billing names of the entries
}

// ErrProblems is returned by Render when Check reports problems.
var ErrProblems = errors.New("message has problems")

// Render renders subject and body for r in mode m (ChildMode or FamilyMode).
// and is the joining word of the interface language ("et" / "and").
//
// The result is plain text only and values are not escaped: a future HTML
// body must escape values when they are substituted.
func Render(subject, body string, r recipients.Recipient, m Mode, and string) (Email, error) {
	if m != ChildMode && m != FamilyMode {
		return Email{}, fmt.Errorf("render: mode %d is not a data mode", m)
	}
	if Check(subject, body, m) != nil {
		return Email{}, ErrProblems
	}
	subject, body = Normalize(subject, body)
	vals := values(r, m, and)
	return Email{
		ParentID:   r.ParentID,
		ToName:     headerSafe(vals[ParentName]),
		To:         r.Email,
		Subject:    headerSafe(substitute(subject, vals)),
		Body:       substitute(body, vals),
		ChildIDs:   append([]int64(nil), r.ChildIDs...),
		ChildNames: append([]string(nil), r.Children...),
	}, nil
}

// substitute replaces the tokens of text. Values are appended and never
// scanned again. Check has already refused unknown tokens and malformed
// text, so a missing value cannot happen; it is kept as written if it does.
func substitute(text string, vals map[string]string) string {
	var b strings.Builder
	for _, seg := range scan(text) {
		switch {
		case seg.isToken:
			if v, ok := vals[seg.name]; ok {
				b.WriteString(v)
			} else {
				b.WriteString(seg.raw)
			}
		case seg.malformed:
			b.WriteString(seg.raw)
		default:
			b.WriteString(seg.text)
		}
	}
	return b.String()
}

// values returns the value of every placeholder available in m for r.
func values(r recipients.Recipient, m Mode, and string) map[string]string {
	if m == FamilyMode {
		return map[string]string{
			// Built from the selected entries, not from the parent record
			// (which keeps the first billing name of a shared address).
			ParentName:   join(dedupe(r.Children, textnorm.Key), and),
			FamilyNumber: join(dedupe(r.FamilyNumbers, func(s string) string { return s }), and),
		}
	}
	return map[string]string{ParentName: r.ParentName}
}

// dedupe keeps the first of the items that have the same key.
func dedupe(items []string, key func(string) string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, it := range items {
		k := key(it)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, it)
	}
	return out
}

// join returns "A", "A and B" or "A, B and C".
func join(items []string, and string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	last := len(items) - 1
	return strings.Join(items[:last], ", ") + " " + and + " " + items[last]
}

// headerSafe makes s fit in an email header: control characters become
// spaces, runs of spaces collapse and the ends are trimmed.
func headerSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
