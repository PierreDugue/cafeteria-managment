package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/store"
)

const (
	// batchHistoryLimit is the number of batches listed on the history page.
	batchHistoryLimit   = 100
	maxHistoryFormBytes = 1 << 10
)

type historyData struct {
	Batches []store.Batch
	Total   int
	Limit   int
	Error   string
}

type batchData struct {
	Batch    store.Batch
	Messages []store.Message
	Done     int  // sent + failed: the progress bar value
	Test     bool // the batch was sent by the test sender
	Started  bool // notices, from the query string after a redirect
	Stopped  bool
	Resumed  bool
	Retried  int
}

type messageData struct {
	Batch   store.Batch
	Message store.Message
	Test    bool
}

// handleHistory lists the last batches, newest first (SPEC §4.5).
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	batches, total, err := s.store.Batches(r.Context(), batchHistoryLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "history", "history", historyData{Batches: batches, Total: total, Limit: batchHistoryLimit})
}

// historyNotFound answers an unknown batch or message with the history page.
func (s *Server) historyNotFound(w http.ResponseWriter, r *http.Request) {
	batches, total, err := s.store.Batches(r.Context(), batchHistoryLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusNotFound, "history", "history", historyData{
		Batches: batches, Total: total, Limit: batchHistoryLimit,
		Error: s.localizer(r).T("history.error.not_found"),
	})
}

// loadBatch reads the batch of the {id} route; ok=false once it has answered.
func (s *Server) loadBatch(w http.ResponseWriter, r *http.Request) (store.Batch, bool) {
	id, ok := parsePathID(r)
	if !ok {
		s.historyNotFound(w, r)
		return store.Batch{}, false
	}
	b, err := s.store.Batch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.historyNotFound(w, r)
		return store.Batch{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return store.Batch{}, false
	}
	return b, true
}

// handleHistoryBatch is the batch page and the progress page: it only reads,
// and refreshes itself (meta refresh) while the batch is sending.
func (s *Server) handleHistoryBatch(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadBatch(w, r)
	if !ok {
		return
	}
	msgs, err := s.store.BatchMessages(r.Context(), b.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	data := batchData{
		Batch:    b,
		Messages: msgs,
		Done:     b.Sent + b.Failed,
		Test:     b.Provider == mailer.ProviderTest,
		Started:  q.Get("started") == "1",
		Stopped:  q.Get("stopped") == "1",
		Resumed:  q.Get("resumed") == "1",
	}
	if n, err := strconv.Atoi(q.Get("retried")); err == nil && n > 0 {
		data.Retried = n
	}
	s.render(w, r, http.StatusOK, "history_batch", "history", data)
}

// handleHistoryMessage shows one email exactly as stored.
func (s *Server) handleHistoryMessage(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadBatch(w, r)
	if !ok {
		return
	}
	mid, ok := pathInt(r, "mid")
	if !ok {
		s.historyNotFound(w, r)
		return
	}
	m, err := s.store.Message(r.Context(), b.ID, mid)
	if errors.Is(err, store.ErrNotFound) {
		s.historyNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "history_message", "history", messageData{Batch: b, Message: m, Test: b.Provider == mailer.ProviderTest})
}

// batchAction runs a POST action on the batch of the {id} route and
// redirects back to its page with the query string it returns.
func (s *Server) batchAction(w http.ResponseWriter, r *http.Request, name string, do func(id int64) (query string, err error)) {
	id, ok := parsePathID(r)
	if !ok {
		s.historyNotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHistoryFormBytes)
	query, err := do(id)
	if errors.Is(err, store.ErrNotFound) {
		s.historyNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	slog.Info("batch "+name, "batch", id)
	http.Redirect(w, r, fmt.Sprintf("/history/%d%s", id, query), http.StatusSeeOther)
}

func (s *Server) handleBatchStop(w http.ResponseWriter, r *http.Request) {
	s.batchAction(w, r, "stop", func(id int64) (string, error) {
		if err := s.store.StopBatch(r.Context(), id, store.StopUser); err != nil {
			return "", err
		}
		return "?stopped=1", nil
	})
}

func (s *Server) handleBatchResume(w http.ResponseWriter, r *http.Request) {
	s.batchAction(w, r, "resume", func(id int64) (string, error) {
		resumed, err := s.store.ResumeBatch(r.Context(), id)
		if err != nil || !resumed {
			return "", err
		}
		s.queue.Wake()
		return "?resumed=1", nil
	})
}

func (s *Server) handleBatchRetry(w http.ResponseWriter, r *http.Request) {
	s.batchAction(w, r, "retry", func(id int64) (string, error) {
		n, err := s.store.RetryFailed(r.Context(), id)
		if err != nil || n == 0 {
			return "", err
		}
		s.queue.Wake()
		return fmt.Sprintf("?retried=%d", n), nil
	})
}

// batchStatusText is the localized status of a batch.
func batchStatusText(l i18n.Localizer, b store.Batch) string {
	switch b.Status {
	case store.BatchStopped:
		return l.T("history.batch.stopped_" + b.StopReason)
	case store.BatchDone:
		return l.T("history.batch.done")
	default:
		return l.T("history.batch.sending")
	}
}

// messageStatusText is the localized status of a message. In test mode a
// sent message is only "recorded": nothing left the computer.
func messageStatusText(l i18n.Localizer, provider string, m store.Message) string {
	if m.Status == store.MsgSent && provider == mailer.ProviderTest {
		return l.T("history.status.sent_test")
	}
	return l.T("history.status." + m.Status)
}

// messageErrorText is the localized type of a failure, "" if none.
func messageErrorText(l i18n.Localizer, m store.Message) string {
	if m.ErrorCode == "" {
		return ""
	}
	return l.T("history.error." + m.ErrorCode)
}
