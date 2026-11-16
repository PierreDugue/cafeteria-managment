package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/store"
)

var historyID = regexp.MustCompile(`^/history/(\d+)`)

// startBatch runs the confirmation and send flow; returns the batch ID.
func (e *sendEnv) startBatch(t *testing.T) int64 {
	t.Helper()
	loc, _ := e.start(t)
	m := historyID.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("location = %q", loc)
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

// claimAll claims every message and applies f to each (1-based index).
func (e *sendEnv) claimAll(t *testing.T, f func(i int, q store.QueuedMessage) error) {
	t.Helper()
	ctx := context.Background()
	for i := 1; ; i++ {
		q, ok, err := e.st.ClaimNextMessage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
		if err := f(i, q); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *sendEnv) sendAll(t *testing.T) {
	e.claimAll(t, func(_ int, q store.QueuedMessage) error { return e.st.MarkSent(context.Background(), q.ID) })
}

const refresh = `http-equiv="refresh"`

func TestHistoryEmpty(t *testing.T) {
	e := newSendEnv(t)
	rec := get(e.s, "/history", "fr")
	if rec.Code != http.StatusOK || !strings.Contains(text(rec), "Aucun envoi pour l'instant.") || !strings.Contains(rec.Body.String(), `href="/history" aria-current="page"`) {
		t.Fatalf("status = %d: %s", rec.Code, text(rec))
	}
	if rec := get(e.s, "/history", "en"); !strings.Contains(text(rec), "No sending yet.") {
		t.Error("english empty state missing")
	}
}

func TestHistoryListLines(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	rec := get(e.s, "/history", "fr")
	body := text(rec)
	for _, want := range []string{"Menu pour {{parent_name}}", "Cantine CE1", "0 / 0 / 2", "Envoi en cours", "Test", fmt.Sprintf(`href="/history/%d"`, id)} {
		if !strings.Contains(body, want) {
			t.Errorf("list does not contain %q", want)
		}
	}
	if strings.Contains(body, "Seuls les") {
		t.Error("limit note shown for one batch")
	}
	e.sendAll(t)
	if body := text(get(e.s, "/history", "fr")); !strings.Contains(body, "2 / 0 / 0") || !strings.Contains(body, "Terminé") {
		t.Errorf("done line missing: %s", body)
	}
}

func TestHistoryLimitNote(t *testing.T) {
	e := newSendEnv(t)
	for i := range batchHistoryLimit + 1 {
		_, _, err := e.st.CreateBatch(context.Background(), store.NewBatch{
			Token: fmt.Sprintf("T%d", i), ListID: e.list, Version: e.version(), Provider: mailer.ProviderTest, Subject: "S", Body: "B",
			Messages: []store.NewMessage{{ToName: "A", ToEmail: "a@example.com", Subject: "S", Body: "B"}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rec := get(e.s, "/history", "fr")
	if !strings.Contains(text(rec), "Seuls les 100 derniers envois sont affichés.") {
		t.Error("limit note missing")
	}
	if n := strings.Count(rec.Body.String(), `<td class="row-actions">`); n != batchHistoryLimit {
		t.Errorf("rows = %d", n)
	}
}

func TestBatchPageRefreshOnlyWhileSending(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	path := fmt.Sprintf("/history/%d", id)

	if !strings.Contains(get(e.s, path, "fr").Body.String(), refresh) {
		t.Error("no refresh while sending")
	}
	if err := e.st.StopBatch(context.Background(), id, store.StopUser); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(get(e.s, path, "fr").Body.String(), refresh) {
		t.Error("refresh while stopped")
	}
	if _, err := e.st.ResumeBatch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	e.sendAll(t)
	if strings.Contains(get(e.s, path, "fr").Body.String(), refresh) {
		t.Error("refresh when done")
	}
}

func TestBatchPageGetNeverChangesState(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	before := snapshot(t, e.st)
	for _, p := range []string{fmt.Sprintf("/history/%d", id), fmt.Sprintf("/history/%d?stopped=1&resumed=1&retried=3", id), "/history", "/"} {
		get(e.s, p, "fr")
	}
	if after := snapshot(t, e.st); after != before {
		t.Errorf("state changed:\n%s\n%s", before, after)
	}
	if e.q.wakeCount() != 1 { // only the send itself
		t.Errorf("wakes = %d", e.q.wakeCount())
	}
}

func TestBatchPageContent(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	path := fmt.Sprintf("/history/%d", id)

	body := text(get(e.s, path, "fr"))
	for _, want := range []string{
		"Mode test", "Envoi en cours", "0 envoyé(s) · 0 en échec · 2 en attente", "Arrêter l'envoi",
		"anne@example.com", "bob@example.com", "Zoé", "Émile", "Liste « Cantine CE1 »", "Actualiser",
		`<progress value="0" max="2">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	for _, not := range []string{"Reprendre l'envoi", "Renvoyer les", "En attente : un autre envoi"} {
		if strings.Contains(body, not) {
			t.Errorf("page contains %q", not)
		}
	}
	if body := text(get(e.s, path+"?started=1", "fr")); !strings.Contains(body, "Envoi lancé.") {
		t.Error("started notice missing")
	}
	if body := text(get(e.s, path, "fr")); strings.Contains(body, "Envoi lancé.") {
		t.Error("started notice without query")
	}
	if body := text(get(e.s, path, "en")); !strings.Contains(body, "Test mode: no email will be sent") || !strings.Contains(body, "Stop sending") && !strings.Contains(body, "Stop") {
		t.Errorf("english page: %s", body)
	}
}

func TestBatchPageButtonsPerState(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	path := fmt.Sprintf("/history/%d", id)
	ctx := context.Background()

	// One failed, one pending, stopped.
	q, _, _ := e.st.ClaimNextMessage(ctx)
	if err := e.st.MarkFailed(ctx, q.ID, store.ErrCodeSend, "refused"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.StopBatch(ctx, id, store.StopUser); err != nil {
		t.Fatal(err)
	}
	body := text(get(e.s, path, "fr"))
	for _, want := range []string{"Arrêté", "Reprendre l'envoi (1 en attente)", "Renvoyer les 1 email(s) en échec", "/history/" + fmt.Sprint(id) + "/resume", "/retry"} {
		if !strings.Contains(body, want) {
			t.Errorf("stopped page does not contain %q", want)
		}
	}
	if strings.Contains(body, "Arrêter l'envoi") {
		t.Error("stop button on a stopped batch")
	}

	// Interrupted wording.
	if _, err := e.st.ResumeBatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.st.StopBatch(ctx, id, store.StopInterrupted); err != nil {
		t.Fatal(err)
	}
	if body := text(get(e.s, path, "fr")); !strings.Contains(body, "Interrompu (l'application a été fermée)") {
		t.Error("interrupted status missing")
	}
	if err := e.st.StopBatch(ctx, id, store.StopAccount); err != nil { // no-op: already stopped
		t.Fatal(err)
	}

	// Done without failure: no buttons.
	e2 := newSendEnv(t)
	id2 := e2.startBatch(t)
	e2.sendAll(t)
	body = text(get(e2.s, fmt.Sprintf("/history/%d", id2), "fr"))
	for _, not := range []string{"Arrêter l'envoi", "Reprendre l'envoi", "Renvoyer les"} {
		if strings.Contains(body, not) {
			t.Errorf("done page contains %q", not)
		}
	}
	if !strings.Contains(body, "Terminé") {
		t.Error("done status missing")
	}
}

func TestBatchPageWaitingNotice(t *testing.T) {
	e := newSendEnv(t)
	first := e.startBatch(t)
	second := e.startBatch(t)
	if !strings.Contains(text(get(e.s, fmt.Sprintf("/history/%d", second), "fr")), "En attente : un autre envoi est en cours.") {
		t.Error("waiting notice missing on the second batch")
	}
	if strings.Contains(text(get(e.s, fmt.Sprintf("/history/%d", first), "fr")), "En attente : un autre envoi") {
		t.Error("waiting notice on the first batch")
	}
}

func TestTestModeStatusWording(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	e.sendAll(t)
	body := text(get(e.s, fmt.Sprintf("/history/%d", id), "fr"))
	if !strings.Contains(body, "Enregistré (test)") {
		t.Error("test status missing")
	}
	if regexp.MustCompile(`>\s*Envoyé\s*<`).MatchString(body) {
		t.Error("a test email shows as Envoyé")
	}
	// A real provider batch says Envoyé.
	q := &fakeQueue{provider: "graph"}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "L", threeChildren)
	bid, _, err := st.CreateBatch(context.Background(), store.NewBatch{Token: "T", ListID: list, Version: versionOf(t, st, list), Provider: "graph", Subject: "S", Body: "B",
		Messages: []store.NewMessage{{ToName: "A", ToEmail: "a@example.com", Subject: "S", Body: "B"}}})
	if err != nil {
		t.Fatal(err)
	}
	cq, _, _ := st.ClaimNextMessage(context.Background())
	if err := st.MarkSent(context.Background(), cq.ID); err != nil {
		t.Fatal(err)
	}
	body = text(get(s, fmt.Sprintf("/history/%d", bid), "fr"))
	if !regexp.MustCompile(`>\s*Envoyé\s*<`).MatchString(body) || strings.Contains(body, "Mode test") {
		t.Errorf("graph batch page: %s", body)
	}
}

func TestErrorCodesLocalizedAndDetailEscaped(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	ctx := context.Background()
	evil := `<script>alert("x")</script> & 550`
	q1, _, _ := e.st.ClaimNextMessage(ctx)
	if err := e.st.MarkFailed(ctx, q1.ID, store.ErrCodeSend, evil); err != nil {
		t.Fatal(err)
	}
	q2, _, _ := e.st.ClaimNextMessage(ctx)
	if err := e.st.MarkFailed(ctx, q2.ID, store.ErrCodeRateLimited, "slow down"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/history/%d", id)
	rec := get(e.s, path, "fr")
	raw := rec.Body.String()
	if strings.Contains(raw, "<script>alert") || !strings.Contains(raw, "&lt;script&gt;alert") {
		t.Errorf("provider text not escaped: %s", raw)
	}
	body := text(rec)
	for _, want := range []string{"Refusé par le fournisseur", "Trop d'emails : le fournisseur a demandé d'attendre", "slow down", "Échec"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if body := text(get(e.s, path, "en")); !strings.Contains(body, "Failed") {
		t.Errorf("english status missing: %s", body)
	}
	// Provider text only on the batch pages: not in the list, the home page.
	for _, p := range []string{"/history", "/"} {
		if strings.Contains(text(get(e.s, p, "fr")), "slow down") || strings.Contains(get(e.s, p, "fr").Body.String(), "alert") {
			t.Errorf("provider text leaked on %s", p)
		}
	}
}

func TestUnknownFailureShowsWarning(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	if _, _, err := e.st.ClaimNextMessage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := text(get(e.s, fmt.Sprintf("/history/%d", id), "fr"))
	for _, want := range []string{"État inconnu (l'application a été fermée pendant l'envoi) : vérifiez vos éléments envoyés avant de renvoyer", "Interrompu", "Reprendre l'envoi (1 en attente)", "Renvoyer les 1 email(s) en échec"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if !strings.Contains(text(get(e.s, "/", "fr")), "1 envoi(s) interrompu(s) : voir l'historique.") {
		t.Error("home notice missing")
	}
}

func TestHomeInterruptedNotice(t *testing.T) {
	e := newSendEnv(t)
	if strings.Contains(text(get(e.s, "/", "fr")), "interrompu") {
		t.Error("notice without interrupted batch")
	}
	id := e.startBatch(t)
	if strings.Contains(text(get(e.s, "/", "fr")), "interrompu") {
		t.Error("notice for a sending batch")
	}
	if err := e.st.StopBatch(context.Background(), id, store.StopUser); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text(get(e.s, "/", "fr")), "interrompu") {
		t.Error("notice for a user-stopped batch")
	}
	if _, err := e.st.ResumeBatch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := e.st.StopBatch(context.Background(), id, store.StopInterrupted); err != nil {
		t.Fatal(err)
	}
	body := text(get(e.s, "/", "fr"))
	if !strings.Contains(body, "1 envoi(s) interrompu(s)") || !strings.Contains(body, `href="/history"`) {
		t.Error("notice missing")
	}
	if !strings.Contains(text(get(e.s, "/", "en")), "1 interrupted sending(s)") {
		t.Error("english notice missing")
	}
}

func TestMessagePage(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	ms, _ := e.st.BatchMessages(context.Background(), id)
	path := fmt.Sprintf("/history/%d/messages/%d", id, ms[0].ID)
	rec := get(e.s, path, "fr")
	body := text(rec)
	for _, want := range []string{"anne@example.com", "Menu pour Anne Martin", "Bonjour Anne Martin,", "À bientôt", "Mode test", fmt.Sprintf(`href="/history/%d"`, id)} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if strings.Contains(rec.Body.String(), refresh) {
		t.Error("message page refreshes")
	}
}

func TestMessagePageEscapes(t *testing.T) {
	e := newSendEnv(t)
	bid, _, err := e.st.CreateBatch(context.Background(), store.NewBatch{Token: "X", ListID: e.list, Version: e.version(), Provider: mailer.ProviderTest, Subject: "S", Body: "B",
		Messages: []store.NewMessage{{ToName: `"<b>Eve</b>"`, ToEmail: "e@example.com", Subject: "<i>sujet</i>", Body: "<script>x()</script>"}}})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := e.st.BatchMessages(context.Background(), bid)
	for _, p := range []string{fmt.Sprintf("/history/%d/messages/%d", bid, ms[0].ID), fmt.Sprintf("/history/%d", bid)} {
		raw := get(e.s, p, "fr").Body.String()
		if strings.Contains(raw, "<script>x()") || strings.Contains(raw, "<b>Eve") || strings.Contains(raw, "<i>sujet") {
			t.Errorf("%s: unescaped content", p)
		}
	}
}

func TestHistoryNotFound(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	ms, _ := e.st.BatchMessages(context.Background(), id)
	other := e.startBatch(t)
	for _, p := range []string{
		"/history/9999", "/history/0", "/history/-1", "/history/abc",
		fmt.Sprintf("/history/%d/messages/9999", id), fmt.Sprintf("/history/%d/messages/abc", id), fmt.Sprintf("/history/%d/messages/0", id),
		fmt.Sprintf("/history/%d/messages/%d", other, ms[0].ID), // wrong batch
		fmt.Sprintf("/history/9999/messages/%d", ms[0].ID),
	} {
		rec := get(e.s, p, "fr")
		if rec.Code != http.StatusNotFound || !strings.Contains(text(rec), "Cet envoi n'existe pas.") {
			t.Errorf("%s: status = %d", p, rec.Code)
		}
	}
	for _, a := range []string{"stop", "resume", "retry"} {
		if rec := post(e.s, "/history/9999/"+a, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s unknown: status = %d", a, rec.Code)
		}
		if rec := post(e.s, "/history/abc/"+a, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s bad id: status = %d", a, rec.Code)
		}
	}
}

func TestBatchActions(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	base := fmt.Sprintf("/history/%d", id)
	ctx := context.Background()

	// stop
	rec := post(e.s, base+"/stop", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base+"?stopped=1" {
		t.Fatalf("stop = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if b, _ := e.st.Batch(ctx, id); b.Status != store.BatchStopped || b.StopReason != store.StopUser {
		t.Errorf("batch = %+v", b)
	}
	if !strings.Contains(text(get(e.s, base+"?stopped=1", "fr")), "Envoi arrêté.") {
		t.Error("stopped notice missing")
	}
	wakes := e.q.wakeCount()

	// resume
	rec = post(e.s, base+"/resume", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base+"?resumed=1" {
		t.Fatalf("resume = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if b, _ := e.st.Batch(ctx, id); b.Status != store.BatchSending {
		t.Errorf("batch = %+v", b)
	}
	if e.q.wakeCount() != wakes+1 {
		t.Error("resume did not wake the queue")
	}
	if !strings.Contains(text(get(e.s, base+"?resumed=1", "fr")), "Envoi repris.") {
		t.Error("resumed notice missing")
	}
	// resume of a sending batch: nothing changes, no wake, no notice.
	rec = post(e.s, base+"/resume", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base || e.q.wakeCount() != wakes+1 {
		t.Errorf("idle resume = %d %q, wakes %d", rec.Code, rec.Header().Get("Location"), e.q.wakeCount())
	}

	// retry
	e.claimAll(t, func(i int, q store.QueuedMessage) error {
		return e.st.MarkFailed(ctx, q.ID, store.ErrCodeSend, "x")
	})
	wakes = e.q.wakeCount()
	rec = post(e.s, base+"/retry", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base+"?retried=2" || e.q.wakeCount() != wakes+1 {
		t.Fatalf("retry = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if b, _ := e.st.Batch(ctx, id); b.Status != store.BatchSending || b.Pending != 2 || b.Failed != 0 {
		t.Errorf("batch = %+v", b)
	}
	if !strings.Contains(text(get(e.s, base+"?retried=2", "fr")), "2 email(s) remis en file d'envoi.") {
		t.Error("retried notice missing")
	}
	for _, bad := range []string{"?retried=abc", "?retried=-3", "?retried=0"} {
		if strings.Contains(text(get(e.s, base+bad, "fr")), "remis en file") {
			t.Errorf("%s: notice shown", bad)
		}
	}
	// second retry: nothing failed.
	rec = post(e.s, base+"/retry", nil)
	if rec.Header().Get("Location") != base || e.q.wakeCount() != wakes+1 {
		t.Errorf("second retry = %q, wakes %d", rec.Header().Get("Location"), e.q.wakeCount())
	}
}

func TestBatchActionsArePostOnlyAndCSRFProtected(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	base := fmt.Sprintf("/history/%d", id)
	for _, a := range []string{"stop", "resume", "retry"} {
		if rec := get(e.s, base+"/"+a, "fr"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d", a, rec.Code)
		}
		req := httptest.NewRequest(http.MethodPost, base+"/"+a, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := do(e.s, req); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site %s = %d", a, rec.Code)
		}
	}
	if b, _ := e.st.Batch(context.Background(), id); b.Status != store.BatchSending {
		t.Errorf("batch changed: %+v", b)
	}
}

func TestBatchActionBodyLimit(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/history/%d/stop", id), strings.NewReader(strings.Repeat("x", 1<<20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := do(e.s, req)
	// The handlers never read the body, so the request still works and the
	// limit only protects against reading it.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestHistorySurvivesListDeletionAndReimport(t *testing.T) {
	e := newSendEnv(t)
	id := e.startBatch(t)
	ms, _ := e.st.BatchMessages(context.Background(), id)
	replaceRows(t, e.st, e.list, []store.ImportRow{{ParentName: "Autre", ChildName: "Autre", Email: "autre@example.com"}})
	if err := e.st.DeleteList(context.Background(), e.list); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/history", fmt.Sprintf("/history/%d", id), fmt.Sprintf("/history/%d/messages/%d", id, ms[0].ID), "/"} {
		rec := get(e.s, p, "fr")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", p, rec.Code)
		}
	}
	body := text(get(e.s, fmt.Sprintf("/history/%d", id), "fr"))
	for _, want := range []string{"Cantine CE1", "anne@example.com", "Zoé", "Émile"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	// Retry works after the list is gone.
	e.claimAll(t, func(_ int, q store.QueuedMessage) error {
		return e.st.MarkFailed(context.Background(), q.ID, store.ErrCodeSend, "x")
	})
	rec := post(e.s, fmt.Sprintf("/history/%d/retry", id), nil)
	if rec.Header().Get("Location") != fmt.Sprintf("/history/%d?retried=2", id) {
		t.Errorf("retry = %q", rec.Header().Get("Location"))
	}
}

func TestFamilyBatchColumns(t *testing.T) {
	q := &fakeQueue{provider: mailer.ProviderTest}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "Familles", silvaRows)
	bid, _, err := st.CreateBatch(context.Background(), store.NewBatch{Token: "F", ListID: list, Version: versionOf(t, st, list), Families: true, Provider: mailer.ProviderTest, Subject: "S", Body: "B",
		Messages: []store.NewMessage{{ToName: "SILVA Ana et SILVA Rui", ToEmail: "silva@example.com", Subject: "S", Body: "B",
			Children: []store.MessageChild{{Name: "SILVA Ana", FamilyNumber: "1234"}, {Name: "SILVA Rui", FamilyNumber: "1250"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	body := text(get(s, fmt.Sprintf("/history/%d", bid), "fr"))
	if !strings.Contains(body, "Familles concernées") || !strings.Contains(body, "1234 SILVA Ana, 1250 SILVA Rui") {
		t.Errorf("family columns missing: %s", body)
	}
	if body := text(get(s, fmt.Sprintf("/history/%d", bid), "en")); !strings.Contains(body, "1234 SILVA Ana, 1250 SILVA Rui") {
		t.Error("english family column missing")
	}
}

// failingSender fails with a secret text, then cancels the context.
type secretSender struct {
	cancel func()
	n      int
}

func (f *secretSender) Send(context.Context, mailer.Message) error {
	f.n++
	if f.n == 1 {
		return fmt.Errorf("550 secret-provider-text for anne@example.com")
	}
	f.cancel()
	return nil
}

func TestNoPersonalDataInLogs(t *testing.T) {
	logs := captureLogs(t)
	e := newSendEnv(t)
	loc, _ := e.start(t)
	id := historyID.FindStringSubmatch(loc)[1]

	ctx, cancel := context.WithCancel(context.Background())
	sender := &secretSender{cancel: cancel}
	wq := mailer.NewQueue(e.st, sender, mailer.Options{Provider: mailer.ProviderTest})
	done := make(chan struct{})
	go func() { wq.Run(ctx); close(done) }()
	<-done

	get(e.s, "/history", "fr")
	get(e.s, "/history/"+id, "fr")
	ms, _ := e.st.BatchMessages(context.Background(), mustID(id))
	get(e.s, fmt.Sprintf("/history/%d/messages/%d", mustID(id), ms[0].ID), "fr")
	post(e.s, "/history/"+id+"/retry", nil)
	post(e.s, "/history/"+id+"/stop", nil)
	post(e.s, "/history/"+id+"/resume", nil)
	get(e.s, "/history/9999", "fr")
	post(e.s, "/send", e.vals("token", "bad token"))

	out := logs.String()
	if !strings.Contains(out, "batch created") {
		t.Fatalf("expected the send log line, got: %s", out)
	}
	for _, secret := range []string{"anne@example.com", "bob@example.com", "Anne Martin", "Bob Durand", "Zoé", "Émile", "Léa", "Menu pour", "Bonjour", "bientôt", "secret-provider-text", "parent_name"} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q:\n%s", secret, out)
		}
	}
}

func mustID(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
