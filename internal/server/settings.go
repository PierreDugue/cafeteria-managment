package server

import (
	"log/slog"
	"net/http"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/microsoft"
	"cafeteria-management/internal/store"
)

const maxSettingsFormBytes = 64 << 10

// languageAuto is the form value of "follow the browser"; it is stored as "".
const languageAuto = "auto"

type settingsData struct {
	Language    string // auto, fr or en: the checked radio
	BrowserLang string // localized name of the browser's language
	Test        bool   // sending is in test mode
	Configured  bool   // this build has Microsoft 365 (client and tenant IDs)
	Connected   bool   // an account is signed in
	Account     microsoft.Account
	Provider    string // test or graph: the checked sending mode radio
	Notice      string
	Error       string
}

// language returns the configured interface language ("" = automatic).
func (s *Server) language() i18n.Lang {
	s.langMu.RLock()
	defer s.langMu.RUnlock()
	return s.lang
}

// provider returns the provider name given to new batches: the sending mode
// (test by default).
func (s *Server) provider() string {
	s.langMu.RLock()
	defer s.langMu.RUnlock()
	if s.mode == mailer.ProviderGraph {
		return mailer.ProviderGraph
	}
	return mailer.ProviderTest
}

func (s *Server) setSettings(l i18n.Lang, mode string) {
	s.langMu.Lock()
	defer s.langMu.Unlock()
	s.lang, s.mode = l, mode
}

// account returns the connected Microsoft 365 account, if any.
func (s *Server) account() (microsoft.Account, bool) {
	if s.ms == nil {
		return microsoft.Account{}, false
	}
	return s.ms.Account()
}

// handleSettings shows the settings. The query holds codes only (never a
// name or an address): ?saved=1 after a save, and after the Microsoft
// actions connected, signedout, tested or signin=denied|consent|expired|failed.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	loc := s.localizer(r)
	q := r.URL.Query()
	notice, errMsg := "", ""
	switch {
	case q.Get("saved") == "1":
		notice = loc.T("settings.saved")
	case s.ms == nil:
	case q.Get("connected") == "1":
		notice = loc.T("settings.microsoft.connected_notice")
	case q.Get("signedout") == "1":
		notice = loc.T("settings.microsoft.signedout_notice")
	case q.Get("tested") == "1":
		if acc, ok := s.account(); ok {
			notice = loc.T("settings.microsoft.tested_notice", acc.Address)
		}
	default:
		switch v := q.Get("signin"); v {
		case "denied", "consent", "expired", "failed":
			errMsg = loc.T("settings.microsoft.error." + v)
		}
	}
	s.renderSettings(w, r, http.StatusOK, errMsg, notice)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, errMsg, notice string) {
	loc := s.localizer(r)
	lang := languageAuto
	if l := s.language(); l != "" {
		lang = string(l)
	}
	browser := s.i18n.Match(r.Header.Get("Accept-Language"))
	acc, connected := s.account()
	s.render(w, r, status, "settings", "settings", settingsData{
		Language:    lang,
		BrowserLang: loc.T("settings.language." + string(browser)),
		Test:        s.provider() == mailer.ProviderTest,
		Configured:  s.ms != nil,
		Connected:   connected,
		Account:     acc,
		Provider:    s.provider(),
		Notice:      notice,
		Error:       errMsg,
	})
}

// handleSettingsSave saves the interface language and the sending mode. The
// store is written first; the in-memory values change only if that
// succeeded.
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

	// Without Microsoft 365 the mode is not in the form: it stays test. A
	// missing value keeps the current mode.
	mode := ""
	if s.provider() == mailer.ProviderGraph {
		mode = mailer.ProviderGraph
	}
	if s.ms != nil {
		switch r.PostForm.Get("sending") {
		case "":
		case mailer.ProviderTest:
			mode = ""
		case mailer.ProviderGraph:
			if _, ok := s.account(); !ok {
				s.renderSettings(w, r, http.StatusUnprocessableEntity, s.localizer(r).T("settings.sending.error.needs_account"), "")
				return
			}
			mode = mailer.ProviderGraph
		default:
			invalid()
			return
		}
	}

	if err := s.store.SaveSettings(r.Context(), store.Settings{Language: string(lang), Provider: mode}); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setSettings(lang, mode)
	slog.Info("settings saved", "language", string(lang), "sending", s.provider())
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
