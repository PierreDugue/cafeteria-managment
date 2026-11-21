package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"time"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/microsoft"
)

// microsoftTimeout bounds the sign-in callback and the test email.
const microsoftTimeout = 30 * time.Second

// microsoftAccount is what the server needs from the Microsoft 365 client
// (satisfied by *microsoft.Client; nil when not configured).
type microsoftAccount interface {
	Account() (microsoft.Account, bool)
	BeginSignIn(returnHost string) (string, error)
	FinishSignIn(ctx context.Context, state, code string) (string, error)
	SignOut() error
	Send(ctx context.Context, m mailer.Message) error
}

// handleMicrosoftSignIn sends the browser to the Microsoft sign-in page, in
// the same tab. The address comes from x/oauth2 on a fixed authority.
func (s *Server) handleMicrosoftSignIn(w http.ResponseWriter, r *http.Request) {
	if s.ms == nil {
		http.NotFound(w, r)
		return
	}
	authURL, err := s.ms.BeginSignIn(r.Host)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleMicrosoftCallback receives the answer of the Microsoft sign-in page.
// The query holds the one-time code: it is never logged.
func (s *Server) handleMicrosoftCallback(w http.ResponseWriter, r *http.Request) {
	if s.ms == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()

	var err error
	switch {
	case q.Get("error") != "":
		err = microsoft.SignInError(q.Get("error"), q.Get("error_description"))
	case q.Get("code") == "" || q.Get("state") == "":
		err = errors.New("callback without code or state")
	}
	returnHost := ""
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), microsoftTimeout)
		defer cancel()
		returnHost, err = s.ms.FinishSignIn(ctx, q.Get("state"), q.Get("code"))
	}

	if err == nil {
		slog.Info("microsoft sign-in", "result", "ok")
		// Back to the origin the user came from (same host and port).
		u := url.URL{Scheme: "http", Host: returnHost, Path: "/settings", RawQuery: "connected=1"}
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return
	}

	result := "failed"
	switch {
	case errors.Is(err, microsoft.ErrSignInExpired):
		result = "expired"
	case errors.Is(err, microsoft.ErrConsent):
		result = "consent"
	case errors.Is(err, microsoft.ErrDenied):
		result = "denied"
	}
	if result == "failed" {
		slog.Warn("microsoft sign-in", "result", result, "err", err)
	} else {
		slog.Info("microsoft sign-in", "result", result)
	}
	// Relative: the return host is unknown when the state is not.
	http.Redirect(w, r, "/settings?signin="+result, http.StatusSeeOther)
}

// handleMicrosoftSignOut forgets the sign-in on this computer.
func (s *Server) handleMicrosoftSignOut(w http.ResponseWriter, r *http.Request) {
	if s.ms == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.ms.SignOut(); err != nil {
		s.serverError(w, r, err)
		return
	}
	slog.Info("microsoft sign-out")
	http.Redirect(w, r, "/settings?signedout=1", http.StatusSeeOther)
}

// handleMicrosoftTest sends one real email to the connected account's own
// address. It does not go through the queue and is not in the history.
func (s *Server) handleMicrosoftTest(w http.ResponseWriter, r *http.Request) {
	if s.ms == nil {
		http.NotFound(w, r)
		return
	}
	loc := s.localizer(r)
	acc, ok := s.ms.Account()
	if !ok {
		s.renderSettings(w, r, http.StatusConflict, loc.T("settings.microsoft.error.not_connected"), "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), microsoftTimeout)
	defer cancel()
	err := s.ms.Send(ctx, mailer.Message{
		To:      mail.Address{Name: acc.Name, Address: acc.Address},
		Subject: loc.T("settings.microsoft.test_subject"),
		Body:    loc.T("settings.microsoft.test_body"),
	})
	if err != nil {
		code := mailer.ErrorCode(err)
		slog.Warn("microsoft test email failed", "code", code)
		// The text is the provider's, for the user's own account; the
		// template escapes it.
		s.renderSettings(w, r, http.StatusBadGateway,
			loc.T("settings.microsoft.error.test_failed", loc.T("history.error."+code), err.Error()), "")
		return
	}
	slog.Info("microsoft test email sent")
	http.Redirect(w, r, "/settings?tested=1", http.StatusSeeOther)
}
