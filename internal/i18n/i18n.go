// Package i18n provides the UI translations, loaded from locales/<lang>.json.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// Lang is a supported interface language.
type Lang string

// Supported languages. French is the default.
const (
	FR Lang = "fr"
	EN Lang = "en"
)

// Default is used when no supported language matches.
const Default = FR

var supported = []Lang{FR, EN}

// matcher picks among supported languages; the first one is the fallback.
var matcher = language.NewMatcher([]language.Tag{language.French, language.English})

// Bundle holds the messages of every supported language.
type Bundle struct {
	msgs map[Lang]map[string]string
}

// Load reads locales/fr.json, locales/en.json, ... from fsys.
// Each file is a flat JSON object of key -> message.
func Load(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{msgs: map[Lang]map[string]string{}}
	for _, l := range supported {
		data, err := fs.ReadFile(fsys, "locales/"+string(l)+".json")
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", l, err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", l, err)
		}
		b.msgs[l] = m
	}
	return b, nil
}

// Parse returns the supported language with exactly this code ("fr", "en").
func Parse(code string) (Lang, bool) {
	for _, l := range supported {
		if code == string(l) {
			return l, true
		}
	}
	return "", false
}

// Match picks the language from an Accept-Language header value, honoring
// the browser's preference order (q values). Defaults to French.
func (b *Bundle) Match(acceptLanguage string) Lang {
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return Default
	}
	tag, _, conf := matcher.Match(tags...)
	if conf == language.No {
		return Default
	}
	base, _ := tag.Base()
	for _, l := range supported {
		if base.String() == string(l) {
			return l
		}
	}
	return Default
}

// Localizer returns the translator for lang.
func (b *Bundle) Localizer(lang Lang) Localizer {
	if _, ok := b.msgs[lang]; !ok {
		lang = Default
	}
	return Localizer{lang: lang, msgs: b.msgs[lang], fallback: b.msgs[Default]}
}

// Localizer translates messages into one language.
type Localizer struct {
	lang     Lang
	msgs     map[string]string
	fallback map[string]string
}

// Lang returns the language code, e.g. for <html lang>.
func (l Localizer) Lang() string { return string(l.lang) }

// T returns the message for key, formatted with args (fmt verbs) if the
// message contains any. Missing keys fall back to French, then to the key.
func (l Localizer) T(key string, args ...any) string {
	msg, ok := l.msgs[key]
	if !ok {
		if msg, ok = l.fallback[key]; !ok {
			return key
		}
	}
	if len(args) > 0 && strings.Contains(msg, "%") {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

// Date formats t in local time, in the usual style of the language.
func (l Localizer) Date(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if l.lang == EN {
		return t.Local().Format("2006-01-02 15:04")
	}
	return t.Local().Format("02/01/2006 15:04")
}
