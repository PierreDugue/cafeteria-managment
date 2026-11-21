package i18n

import (
	"os"
	"sort"
	"testing"
	"testing/fstest"
)

func TestLocalesHaveSameKeys(t *testing.T) {
	b, err := Load(os.DirFS("../../web"))
	if err != nil {
		t.Fatal(err)
	}
	ref := keys(b.msgs[Default])
	for _, l := range supported {
		got := keys(b.msgs[l])
		for _, k := range ref {
			if _, ok := b.msgs[l][k]; !ok {
				t.Errorf("%s: missing key %q", l, k)
			}
		}
		for _, k := range got {
			if _, ok := b.msgs[Default][k]; !ok {
				t.Errorf("%s: extra key %q", l, k)
			}
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTranslate(t *testing.T) {
	fsys := fstest.MapFS{
		"locales/fr.json": {Data: []byte(`{"hello": "Bonjour %s", "only_fr": "Seulement", "plain": "Texte"}`)},
		"locales/en.json": {Data: []byte(`{"hello": "Hello %s", "plain": "Text"}`)},
	}
	b, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}

	en := b.Localizer(EN)
	tests := []struct {
		key  string
		args []any
		want string
	}{
		{"hello", []any{"Léa"}, "Hello Léa"},
		{"only_fr", nil, "Seulement"}, // falls back to French
		{"missing", nil, "missing"},
		{"plain", []any{"ignored"}, "Text"}, // no verb: args ignored
	}
	for _, tt := range tests {
		if got := en.T(tt.key, tt.args...); got != tt.want {
			t.Errorf("T(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestMatch(t *testing.T) {
	b := &Bundle{}
	tests := []struct {
		header string
		want   Lang
	}{
		{"", FR},
		{"en-US,en;q=0.9", EN},
		{"de-DE,en;q=0.8", EN},
		{"fr-FR,fr;q=0.9,en;q=0.8", FR},
		{"de-DE", FR},
		{"en;q=0.1, fr;q=0.9", FR},
		{"fr;q=0.2, en", EN},
		{"not a header;;", FR},
	}
	for _, tt := range tests {
		if got := b.Match(tt.header); got != tt.want {
			t.Errorf("Match(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestParse(t *testing.T) {
	for code, want := range map[string]bool{
		"fr": true, "en": true,
		"": false, "auto": false, "de": false, "FR": false, " fr": false, "fr-FR": false, "fr ": false,
	} {
		l, ok := Parse(code)
		if ok != want || (ok && string(l) != code) || (!ok && l != "") {
			t.Errorf("Parse(%q) = %q, %v; want ok=%v", code, l, ok, want)
		}
	}
}

func TestSettingsKeysInBothLocales(t *testing.T) {
	b, err := Load(os.DirFS("../../web"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range supported {
		for _, k := range []string{"nav.settings", "settings.title", "settings.language.legend", "settings.language.auto",
			"settings.language.fr", "settings.language.en", "settings.language.help", "settings.sending.title",
			"settings.sending.unavailable", "settings.sending.mode.legend", "settings.sending.mode.test",
			"settings.sending.mode.graph", "settings.sending.error.needs_account", "settings.microsoft.title",
			"settings.microsoft.signin", "settings.microsoft.signout", "settings.microsoft.test",
			"settings.microsoft.error.denied", "settings.microsoft.error.consent", "settings.microsoft.error.expired",
			"settings.microsoft.error.failed", "settings.microsoft.error.not_connected", "settings.microsoft.error.test_failed",
			"send.error.mode_changed", "send.error.not_connected", "send.from", "settings.saved", "settings.error.invalid"} {
			if b.msgs[l][k] == "" {
				t.Errorf("%s: key %q missing or empty", l, k)
			}
		}
	}
}
