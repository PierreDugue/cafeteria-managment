package mailer

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cafeteria-management/internal/store"
)

// fakeSender records calls and fails on demand.
type fakeSender struct {
	mu    sync.Mutex
	calls []Message
	ctxs  []context.Context
	fn    func(n int, m Message) error // n is the 1-based call number
	sent  chan struct{}                // optional: one value per call
}

func (f *fakeSender) Send(ctx context.Context, m Message) error {
	f.mu.Lock()
	f.calls = append(f.calls, m)
	f.ctxs = append(f.ctxs, ctx)
	n := len(f.calls)
	fn := f.fn
	f.mu.Unlock()
	var err error
	if fn != nil {
		err = fn(n, m)
	}
	if f.sent != nil {
		f.sent <- struct{}{}
	}
	return err
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type env struct {
	st     *store.Store
	q      *Queue
	sender *fakeSender
	waits  []time.Duration
	listID int64
	ver    int64
}

func newEnv(t *testing.T, interval time.Duration) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rows := []store.ImportRow{{ParentName: "Marie Dupont", ChildName: "Léa", Email: "marie@example.fr"}}
	sum, err := st.ImportFamilies(ctx, store.ImportRequest{FileName: "a.xlsx", RowsRead: 1, Rows: rows, NewListName: "L"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.ListVersion(ctx, sum.ListID)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, sender: &fakeSender{}, listID: sum.ListID, ver: v}
	e.q = NewQueue(st, e.sender, Options{Provider: ProviderTest, Interval: interval})
	e.q.after = func(d time.Duration) <-chan time.Time {
		e.waits = append(e.waits, d)
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	return e
}

func (e *env) batch(t *testing.T, token string, msgs ...store.NewMessage) int64 {
	t.Helper()
	id, _, err := e.st.CreateBatch(context.Background(), store.NewBatch{
		Token: token, ListID: e.listID, Version: e.ver, Provider: ProviderTest,
		Subject: "S", Body: "B", Messages: msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func msg(name, email string) store.NewMessage {
	return store.NewMessage{ToName: name, ToEmail: email, Subject: "Sujet " + name, Body: "Corps"}
}

func (e *env) status(t *testing.T, batchID int64) []store.Message {
	t.Helper()
	ms, err := e.st.BatchMessages(context.Background(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestStepSendsInOrderAndMarksSent(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	tricky := `Éloïse "Lou", <Dupont>`
	id := e.batch(t, "T", msg(tricky, "a@example.fr"), msg("Bob", "b@example.fr"))
	ctx := context.Background()

	worked, wait, err := e.q.step(ctx)
	if err != nil || !worked || wait != 2*time.Second {
		t.Fatalf("step = %v, %v, %v", worked, wait, err)
	}
	if _, _, err := e.q.step(ctx); err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{To: mail.Address{Name: tricky, Address: "a@example.fr"}, Subject: "Sujet " + tricky, Body: "Corps"},
		{To: mail.Address{Name: "Bob", Address: "b@example.fr"}, Subject: "Sujet Bob", Body: "Corps"},
	}
	if fmt.Sprint(e.sender.calls) != fmt.Sprint(want) {
		t.Errorf("calls = %+v", e.sender.calls)
	}
	for _, m := range e.status(t, id) {
		if m.Status != store.MsgSent {
			t.Errorf("message = %+v", m)
		}
	}
	if b, _ := e.st.Batch(ctx, id); b.Status != store.BatchDone {
		t.Errorf("batch = %+v", b)
	}
	// Nothing left: no work, no wait (idle until Wake).
	worked, wait, err = e.q.step(ctx)
	if err != nil || worked || wait != 0 {
		t.Errorf("idle step = %v, %v, %v", worked, wait, err)
	}
}

func TestStepGenericErrorFailsAndGoesOn(t *testing.T) {
	e := newEnv(t, 0)
	e.sender.fn = func(n int, _ Message) error {
		if n == 1 {
			return errors.New("550 mailbox unavailable")
		}
		return nil
	}
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"))
	ctx := context.Background()
	for range 2 {
		if _, _, err := e.q.step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ms := e.status(t, id)
	if ms[0].Status != store.MsgFailed || ms[0].ErrorCode != store.ErrCodeSend || ms[0].Error != "550 mailbox unavailable" {
		t.Errorf("first = %+v", ms[0])
	}
	if ms[1].Status != store.MsgSent {
		t.Errorf("second = %+v", ms[1])
	}
}

func TestStepRateLimit(t *testing.T) {
	tests := []struct {
		name string
		ra   time.Duration
		want time.Duration
	}{
		{"given", 7 * time.Second, 7 * time.Second},
		{"zero gives 60s", 0, 60 * time.Second},
		{"negative gives 60s", -time.Second, 60 * time.Second},
		{"huge capped at 15 min", 48 * time.Hour, 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, time.Second)
			// Wrapped: errors.As must still find it.
			e.sender.fn = func(int, Message) error {
				return fmt.Errorf("graph: %w", &RateLimitError{RetryAfter: tt.ra, Err: errors.New("429")})
			}
			id := e.batch(t, "T", msg("A", "a@example.fr"))
			worked, wait, err := e.q.step(context.Background())
			if err != nil || !worked || wait != tt.want {
				t.Fatalf("step = %v, %v, %v", worked, wait, err)
			}
			if m := e.status(t, id)[0]; m.Status != store.MsgPending || m.Attempts != 1 {
				t.Errorf("message = %+v", m)
			}
		})
	}
}

func TestStepRateLimitFailsOnFifthAttempt(t *testing.T) {
	e := newEnv(t, 0)
	e.sender.fn = func(int, Message) error { return &RateLimitError{RetryAfter: time.Second} }
	id := e.batch(t, "T", msg("A", "a@example.fr"))
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		if _, _, err := e.q.step(ctx); err != nil {
			t.Fatal(err)
		}
		if m := e.status(t, id)[0]; m.Status != store.MsgPending || m.Attempts != i {
			t.Fatalf("attempt %d: %+v", i, m)
		}
	}
	if _, _, err := e.q.step(ctx); err != nil {
		t.Fatal(err)
	}
	m := e.status(t, id)[0]
	if m.Status != store.MsgFailed || m.ErrorCode != store.ErrCodeRateLimited || m.Attempts != 5 {
		t.Errorf("message = %+v", m)
	}
}

func TestStepAccountErrorStopsBatch(t *testing.T) {
	e := newEnv(t, 0)
	e.sender.fn = func(int, Message) error { return fmt.Errorf("not signed in: %w", ErrAccount) }
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"))
	ctx := context.Background()
	if worked, _, err := e.q.step(ctx); err != nil || !worked {
		t.Fatalf("step = %v, %v", worked, err)
	}
	b, _ := e.st.Batch(ctx, id)
	if b.Status != store.BatchStopped || b.StopReason != store.StopAccount || b.Failed != 0 || b.Pending != 2 {
		t.Errorf("batch = %+v", b)
	}
	if worked, _, _ := e.q.step(ctx); worked || e.sender.count() != 1 {
		t.Errorf("second step worked = %v, calls = %d", worked, e.sender.count())
	}
}

func TestStepStopBetweenMessages(t *testing.T) {
	e := newEnv(t, 0)
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"))
	ctx := context.Background()
	if _, _, err := e.q.step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.st.StopBatch(ctx, id, store.StopUser); err != nil {
		t.Fatal(err)
	}
	if worked, _, err := e.q.step(ctx); err != nil || worked {
		t.Fatalf("step = %v, %v", worked, err)
	}
	if e.sender.count() != 1 {
		t.Errorf("calls = %d", e.sender.count())
	}
}

func TestStepCancelledBeforeClaimClaimsNothing(t *testing.T) {
	e := newEnv(t, 0)
	id := e.batch(t, "T", msg("A", "a@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if worked, wait, err := e.q.step(ctx); worked || wait != 0 || err != nil {
		t.Fatalf("step = %v, %v, %v", worked, wait, err)
	}
	if m := e.status(t, id)[0]; m.Status != store.MsgPending || m.Attempts != 0 {
		t.Errorf("message = %+v", m)
	}
}

func TestStepCancelDuringSendStillRecords(t *testing.T) {
	e := newEnv(t, 0)
	id := e.batch(t, "T", msg("A", "a@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	e.sender.fn = func(int, Message) error {
		cancel() // the app is closing while the email is in flight
		return nil
	}
	worked, _, err := e.q.step(ctx)
	if err != nil || !worked {
		t.Fatalf("step = %v, %v", worked, err)
	}
	if m := e.status(t, id)[0]; m.Status != store.MsgSent {
		t.Errorf("message = %+v", m)
	}
}

func TestSendContextHasDeadlineAndSurvivesCancel(t *testing.T) {
	e := newEnv(t, 0)
	e.q.opts.SendTimeout = time.Minute
	e.batch(t, "T", msg("A", "a@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	var during error
	e.sender.fn = func(int, Message) error {
		cancel()
		during = e.sender.ctxs[0].Err()
		return nil
	}
	if _, _, err := e.q.step(ctx); err != nil {
		t.Fatal(err)
	}
	if during != nil {
		t.Errorf("send context cancelled with the parent: %v", during)
	}
	if _, ok := e.sender.ctxs[0].Deadline(); !ok {
		t.Error("send context has no deadline")
	}
}

// failingStore fails every call.
type failingStore struct{ calls int }

var errDB = errors.New("database is locked")

func (f *failingStore) ClaimNextMessage(context.Context) (store.QueuedMessage, bool, error) {
	f.calls++
	return store.QueuedMessage{}, false, errDB
}
func (f *failingStore) MarkSent(context.Context, int64) error                   { return errDB }
func (f *failingStore) MarkFailed(context.Context, int64, string, string) error { return errDB }
func (f *failingStore) ReleaseMessage(context.Context, int64) error             { return errDB }
func (f *failingStore) StopBatch(context.Context, int64, string) error          { return errDB }
func (f *failingStore) RecoverInterrupted(context.Context) error                { return errDB }

func TestStepStoreErrorWaitsFiveSeconds(t *testing.T) {
	q := NewQueue(&failingStore{}, &fakeSender{}, Options{})
	worked, wait, err := q.step(context.Background())
	if worked || wait != 5*time.Second || !errors.Is(err, errDB) {
		t.Fatalf("step = %v, %v, %v", worked, wait, err)
	}
	if err := q.Recover(context.Background()); !errors.Is(err, errDB) {
		t.Errorf("Recover = %v", err)
	}
}

// recordFailStore claims fine but fails to record.
type recordFailStore struct{ failingStore }

func (r *recordFailStore) ClaimNextMessage(context.Context) (store.QueuedMessage, bool, error) {
	return store.QueuedMessage{ID: 1, BatchID: 1, ToEmail: "a@example.fr", Attempts: 1}, true, nil
}

func TestStepRecordErrorWaitsAtLeastFiveSeconds(t *testing.T) {
	q := NewQueue(&recordFailStore{}, &fakeSender{}, Options{Interval: time.Second})
	worked, wait, err := q.step(context.Background())
	if !worked || wait < 5*time.Second || !errors.Is(err, errDB) {
		t.Fatalf("step = %v, %v, %v", worked, wait, err)
	}
}

func TestRunStoreErrorDoesNotSpin(t *testing.T) {
	fs := &failingStore{}
	q := NewQueue(fs, &fakeSender{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	q.after = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		if len(waits) == 3 {
			cancel()
		}
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	q.Run(ctx)
	if len(waits) != 3 || fs.calls != 3 {
		t.Fatalf("waits = %v, claims = %d", waits, fs.calls)
	}
	for _, w := range waits {
		if w != 5*time.Second {
			t.Errorf("wait = %v", w)
		}
	}
}

func TestRunSendsAllWithIntervalThenIdles(t *testing.T) {
	e := newEnv(t, 3*time.Second)
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	e.sender.sent = make(chan struct{}, 10)
	done := make(chan struct{})
	go func() { e.q.Run(ctx); close(done) }()
	<-e.sender.sent
	<-e.sender.sent
	cancel()
	<-done // goroutine exits
	if e.sender.count() != 2 {
		t.Errorf("calls = %d", e.sender.count())
	}
	for _, m := range e.status(t, id) {
		if m.Status != store.MsgSent {
			t.Errorf("message = %+v", m)
		}
	}
}

func TestRunCancelDuringSendRecordsAndLeavesRestPending(t *testing.T) {
	e := newEnv(t, 0)
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"), msg("C", "c@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	e.sender.fn = func(n int, _ Message) error {
		if n == 2 {
			cancel()
		}
		return nil
	}
	done := make(chan struct{})
	go func() { e.q.Run(ctx); close(done) }()
	<-done
	ms := e.status(t, id)
	got := []string{ms[0].Status, ms[1].Status, ms[2].Status}
	want := []string{store.MsgSent, store.MsgSent, store.MsgPending}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

func TestRunIdleReturnsOnCancel(t *testing.T) {
	e := newEnv(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.q.Run(ctx); close(done) }()
	cancel()
	<-done
}

func TestRunWakesOnNewBatch(t *testing.T) {
	e := newEnv(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	e.sender.sent = make(chan struct{}, 10)
	done := make(chan struct{})
	go func() { e.q.Run(ctx); close(done) }()

	id := e.batch(t, "T", msg("A", "a@example.fr"))
	e.q.Wake()
	<-e.sender.sent
	cancel()
	<-done
	if m := e.status(t, id)[0]; m.Status != store.MsgSent {
		t.Errorf("message = %+v", m)
	}
}

func TestRunRateLimitWaitUsesTimerOnly(t *testing.T) {
	e := newEnv(t, 0)
	id := e.batch(t, "T", msg("A", "a@example.fr"))
	ctx, cancel := context.WithCancel(context.Background())
	e.sender.fn = func(n int, _ Message) error {
		if n == 1 {
			return &RateLimitError{RetryAfter: 30 * time.Second}
		}
		cancel()
		return nil
	}
	done := make(chan struct{})
	go func() { e.q.Run(ctx); close(done) }()
	<-done
	if len(e.waits) != 1 || e.waits[0] != 30*time.Second {
		t.Errorf("waits = %v", e.waits)
	}
	if m := e.status(t, id)[0]; m.Status != store.MsgSent || m.Attempts != 2 {
		t.Errorf("message = %+v", m)
	}
}

func TestWakeNeverBlocks(t *testing.T) {
	q := NewQueue(&failingStore{}, &fakeSender{}, Options{})
	finished := make(chan struct{})
	go func() {
		for range 1000 {
			q.Wake()
		}
		close(finished)
	}()
	<-finished
}

func TestRecoverDelegatesAndAccessors(t *testing.T) {
	e := newEnv(t, 500*time.Millisecond)
	id := e.batch(t, "T", msg("A", "a@example.fr"), msg("B", "b@example.fr"))
	ctx := context.Background()
	if _, _, err := e.q.step(ctx); err == nil {
		// first message sent; claim the second but do not record: simulate crash.
		if _, ok, err := e.st.ClaimNextMessage(ctx); err != nil || !ok {
			t.Fatal(err)
		}
	}
	if err := e.q.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.st.Batch(ctx, id); b.Status != store.BatchDone || b.Failed != 1 {
		t.Errorf("batch = %+v", b)
	}
	if e.q.Provider() != ProviderTest || e.q.Interval() != 500*time.Millisecond {
		t.Errorf("Provider/Interval = %q %v", e.q.Provider(), e.q.Interval())
	}
}

func TestTestSenderAndErrors(t *testing.T) {
	if err := (TestSender{}).Send(context.Background(), Message{}); err != nil {
		t.Fatal(err)
	}
	inner := errors.New("429")
	rl := &RateLimitError{RetryAfter: time.Second, Err: inner}
	if !errors.Is(rl, inner) || rl.Error() == "" || (&RateLimitError{}).Error() == "" {
		t.Error("RateLimitError")
	}
}
