// Package textnorm normalizes text so that values typed slightly differently
// (case, accents, extra spaces) can be compared.
package textnorm

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Clean trims s and collapses internal whitespace to single spaces.
func Clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Key returns a comparison key for s: cleaned, lower-case and without accents.
// "  Léa  MARTIN " and "lea martin" have the same key.
func Key(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	return strings.ToLower(Clean(out))
}
