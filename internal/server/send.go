package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/message"
	"cafeteria-management/internal/store"
)

// maxTokenLen bounds a confirmation token (rand.Text gives 26 characters).
const maxTokenLen = 64

// queue is what the server needs from the background sender.
type queue interface {
	Wake()
	Interval(provider string) time.Duration
}

// sendConfirmData is the confirmation page of a sending (SPEC §4.4).
type sendConfirmData struct {
	ListID     int64
	ListName   string
	Version    int64
	IDs        []int64
	Subject    string
	Body       string
	Families   bool
	Count      int
	Recipients []recipientRow
	Example    *message.Email
	Token      string // one-time token of this page
	Provider   string // sending provider this page was shown for
	Test       bool
	From       string // "Name <address>" of the sending account, Graph mode only
	Minutes    int
	Notice     string
}

// isToken reports whether s looks like a token made by crypto/rand.Text:
// 1 to maxTokenLen characters of [A-Z2-7].
func isToken(s string) bool {
	if s == "" || len(s) > maxTokenLen {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// sendMinutes estimates the sending time in minutes, at least 1.
func sendMinutes(count int, interval time.Duration) int {
	m := int((time.Duration(count)*interval + time.Minute - 1) / time.Minute)
	return max(m, 1)
}

// renderSendConfirm shows the confirmation page for already checked emails.
// Nothing is written: the token only becomes "used" when a batch has it.
func (s *Server) renderSendConfirm(w http.ResponseWriter, r *http.Request, status int, sel selection, subject, body string, emails []message.Email, notice string) {
	provider := s.provider()
	data := sendConfirmData{
		ListID:     sel.listID,
		ListName:   sel.list.Name,
		Version:    sel.version,
		IDs:        entryIDs(sel.children),
		Subject:    subject,
		Body:       body,
		Families:   sel.mode == message.FamilyMode,
		Count:      len(emails),
		Recipients: recipientRows(sel.group, sel.mode),
		Example:    &emails[0],
		Token:      rand.Text(),
		Provider:   provider,
		Test:       provider == mailer.ProviderTest,
		Minutes:    sendMinutes(len(emails), s.queue.Interval(provider)),
		Notice:     notice,
	}
	if provider == mailer.ProviderGraph {
		if acc, ok := s.account(); ok {
			// By hand: mail.Address.String would RFC 2047-encode accents.
			data.From = acc.Address
			if acc.Name != "" {
				data.From = acc.Name + " <" + acc.Address + ">"
			}
		}
	}
	s.render(w, r, status, "send_confirm", "children", data)
}

// handleSend re-checks everything, then stores the batch and its emails in
// one transaction and hands them to the queue (SPEC §4.4).
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.readSelection(w, r)
	if !ok {
		return
	}
	loc := s.localizer(r)
	subject, body := message.Normalize(r.PostForm.Get("subject"), r.PostForm.Get("body"))
	data := composeBase(loc, sel)
	data.Subject, data.Body = subject, body

	token := r.PostForm.Get("token")
	if !isToken(token) {
		data.Errors = []string{loc.T("send.error.no_token")}
		s.renderCompose(w, r, http.StatusUnprocessableEntity, data)
		return
	}

	if msg := s.sendBlocked(loc); msg != "" {
		data.Errors = []string{msg}
		s.renderCompose(w, r, http.StatusUnprocessableEntity, data)
		return
	}

	emails, problems, err := message.RenderAll(subject, body, sel.group, sel.mode, loc.T("message.join_and"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if problems != nil {
		data.Problems = problemLines(loc, problems)
		s.renderCompose(w, r, http.StatusUnprocessableEntity, data)
		return
	}

	// The page said which mode it was for: a mode changed since then must
	// never create a batch the user did not confirm (SPEC §4.4).
	provider := s.provider()
	if r.PostForm.Get("provider") != provider {
		s.renderSendConfirm(w, r, http.StatusConflict, sel, subject, body, emails, loc.T("send.error.mode_changed"))
		return
	}

	nb := store.NewBatch{
		Token:    token,
		ListID:   sel.listID,
		Version:  sel.version,
		Families: sel.mode == message.FamilyMode,
		Provider: provider,
		Subject:  subject,
		Body:     body,
		Messages: make([]store.NewMessage, len(emails)),
	}
	for i, e := range emails {
		m := store.NewMessage{ParentID: e.ParentID, ToName: e.ToName, ToEmail: e.To, Subject: e.Subject, Body: e.Body}
		for j, id := range e.ChildIDs {
			m.Children = append(m.Children, store.MessageChild{ChildID: id, Name: e.ChildNames[j], FamilyNumber: e.FamilyNumbers[j]})
		}
		nb.Messages[i] = m
	}

	id, created, err := s.store.CreateBatch(r.Context(), nb)
	if errors.Is(err, store.ErrStaleSelection) {
		s.renderChildren(w, r, http.StatusConflict, sel.listID, loc.T("compose.error.stale"))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !created { // same confirmation page posted again
		http.Redirect(w, r, fmt.Sprintf("/history/%d", id), http.StatusSeeOther)
		return
	}
	slog.Info("batch created", "batch", id, "list", sel.listID, "recipients", len(emails))
	s.queue.Wake()
	http.Redirect(w, r, fmt.Sprintf("/history/%d?started=1", id), http.StatusSeeOther)
}

// sendBlocked returns the localized reason why nothing can be sent now, ""
// if sending may go on: Microsoft 365 mode needs a connected account.
func (s *Server) sendBlocked(loc i18n.Localizer) string {
	if s.provider() == mailer.ProviderGraph {
		if _, ok := s.account(); !ok {
			return loc.T("send.error.not_connected")
		}
	}
	return ""
}
