package server

import (
	"log/slog"
	"net/http"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/store"
)

const maxSettingsFormBytes = 64 << 10

// languageAuto is the form value of "follow the browser"; it is stored as "".
const languageAuto = "auto"

type settingsData struct {
	Language    string // auto, fr or en: the checked radio
	BrowserLang string // localized name of the browser's language
	Test        bool   // sending is in test mode
	Notice      string
	Error       string
}

// language returns the configured interface language ("" = automatic).
func (s *Server) language() i18n.Lang {
	s.langMu.RLock()
	defer s.langMu.RUnlock()
	return s.lang
}

func (s *Server) setLanguage(l i18n.Lang) {
	s.langMu.Lock()
	defer s.langMu.Unlock()
	s.lang = l
}

// handleSettings shows the settings; ?saved=1 shows a notice after a save.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	notice := ""
	if r.URL.Query().Get("saved") == "1" {
		notice = s.localizer(r).T("settings.saved")
	}
	s.renderSettings(w, r, http.StatusOK, "", notice)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, errMsg, notice string) {
	loc := s.localizer(r)
	lang := languageAuto
	if l := s.language(); l != "" {
		lang = string(l)
	}
	browser := s.i18n.Match(r.Header.Get("Accept-Language"))
	s.render(w, r, status, "settings", "settings", settingsData{
		Language:    lang,
		BrowserLang: loc.T("settings.language." + string(browser)),
		Test:        s.queue.Provider() == mailer.ProviderTest,
		Notice:      notice,
		Error:       errMsg,
	})
}

// handleSettingsSave saves the interface language. The store is written
// first; the in-memory value changes only if that succeeded.
func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsFormBytes)
	invalid := func() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, s.localizer(r).T("settings.error.invalid"), "")
	}
	if err := r.ParseForm(); err != nil {
		invalid()
		return
	}
	var lang i18n.Lang
	switch v := r.PostForm.Get("language"); v {
	case languageAuto:
	default:
		l, ok := i18n.Parse(v)
		if !ok {
			invalid()
			return
		}
		lang = l
	}
	if err := s.store.SaveSettings(r.Context(), store.Settings{Language: string(lang)}); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setLanguage(lang)
	slog.Info("settings saved", "language", string(lang))
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
