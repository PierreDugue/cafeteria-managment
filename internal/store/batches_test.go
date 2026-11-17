package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var rowsTwoFamilies = []ImportRow{
	{ParentName: "Marie Dupont", ChildName: "Léa Dupont", Email: "marie@example.fr"},
	{ParentName: "Marie Dupont", ChildName: "Tom Dupont", Email: "marie@example.fr"},
	{ParentName: "Paul Martin", ChildName: "Zoé Martin", Email: "paul@example.fr"},
}

// batchEnv is a store with a list and its entries, ready for batches.
type batchEnv struct {
	s       *Store
	listID  int64
	version int64
	kids    map[string]Child
}

func newBatchEnv(t *testing.T) *batchEnv {
	t.Helper()
	s := openTest(t)
	tick(s)
	id := newList(t, s, "Cantine CE1", rowsTwoFamilies)
	v, err := s.ListVersion(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	kids := map[string]Child{}
	for _, c := range listEntries(t, s, id) {
		kids[c.Name] = c
	}
	return &batchEnv{s: s, listID: id, version: v, kids: kids}
}

// batch builds a NewBatch with n messages (n <= 3).
func (e *batchEnv) batch(token string, n int) NewBatch {
	all := []NewMessage{
		{ParentID: e.kids["Léa Dupont"].ParentID, ToName: "Marie Dupont", ToEmail: "marie@example.fr", Subject: "Menu de Marie", Body: "Bonjour Marie",
			Children: []MessageChild{{ChildID: e.kids["Léa Dupont"].ID, Name: "Léa Dupont", FamilyNumber: "10"}, {ChildID: e.kids["Tom Dupont"].ID, Name: "Tom Dupont"}}},
		{ParentID: e.kids["Zoé Martin"].ParentID, ToName: "Paul Martin", ToEmail: "paul@example.fr", Subject: "Menu de Paul", Body: "Bonjour Paul",
			Children: []MessageChild{{ChildID: e.kids["Zoé Martin"].ID, Name: "Zoé Martin"}}},
		{ToName: "Anne", ToEmail: "anne@example.fr", Subject: "S3", Body: ""},
	}
	return NewBatch{Token: token, ListID: e.listID, Version: e.version, Provider: "test",
		Subject: "Menu {{parent_name}}", Body: "Bonjour {{parent_name}}", Messages: all[:n]}
}

func (e *batchEnv) create(t *testing.T, token string, n int) int64 {
	t.Helper()
	id, created, err := e.s.CreateBatch(context.Background(), e.batch(token, n))
	if err != nil || !created {
		t.Fatalf("CreateBatch = %d, %v, %v", id, created, err)
	}
	return id
}

func (e *batchEnv) msgIDs(t *testing.T, batchID int64) []int64 {
	t.Helper()
	ms, err := e.s.BatchMessages(context.Background(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}

func (e *batchEnv) msg(t *testing.T, batchID, id int64) Message {
	t.Helper()
	m, err := e.s.Message(context.Background(), batchID, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *batchEnv) batchOf(t *testing.T, id int64) Batch {
	t.Helper()
	b, err := e.s.Batch(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *batchEnv) claim(t *testing.T) QueuedMessage {
	t.Helper()
	q, ok, err := e.s.ClaimNextMessage(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim = %+v, %v, %v", q, ok, err)
	}
	return q
}

func (e *batchEnv) noClaim(t *testing.T) {
	t.Helper()
	if q, ok, err := e.s.ClaimNextMessage(context.Background()); err != nil || ok {
		t.Fatalf("claim = %+v, %v, %v; want nothing", q, ok, err)
	}
}

// ---- migration 006 ----------------------------------------------------------

func TestMigration006OnV5Database(t *testing.T) {
	ctx := context.Background()
	path := buildV4(t, true)
	s, err := Open(ctx, path) // now at the latest version
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Go back to version 5: remove what 006 and 007 created.
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE message_children`, `DROP TABLE messages`, `DROP TABLE batches`, `DROP TABLE settings`, `DELETE FROM schema_migrations WHERE version IN (6, 7)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("setup version = %d, %v", v, err)
	}
	db.Close()

	s = openPath(t, path)
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != latestSchemaVersion {
		t.Errorf("version = %d, %v", v, err)
	}
	ls, err := s.Lists(ctx)
	if err != nil || len(ls) != 1 || ls[0].Parents != 2 || ls[0].Children != 5 {
		t.Errorf("lists = %+v, %v", ls, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports`); n != 2 {
		t.Errorf("imports = %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM templates`); n != 1 {
		t.Errorf("templates = %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM last_selection`); n != 2 {
		t.Errorf("last_selection = %d", n)
	}
	for _, table := range []string{"batches", "messages", "message_children"} {
		if n := countRows(t, s, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows", table, n)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
}

// latestSchemaVersion is the version of the newest migration.
const latestSchemaVersion = 7

func TestFreshDatabaseIsAtLatestVersion(t *testing.T) {
	s := openTest(t)
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != latestSchemaVersion {
		t.Fatalf("version = %d, %v", v, err)
	}
}

// ---- CreateBatch --------------------------------------------------------------

func TestCreateBatchStoresCopies(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	nb := e.batch("TOKEN1", 3)
	nb.Families = true
	id, created, err := e.s.CreateBatch(ctx, nb)
	if err != nil || !created || id == 0 {
		t.Fatalf("CreateBatch = %d, %v, %v", id, created, err)
	}

	b := e.batchOf(t, id)
	if b.ListID != e.listID || b.ListName != "Cantine CE1" || !b.Families || b.Provider != "test" ||
		b.Subject != nb.Subject || b.Body != nb.Body || b.Status != BatchSending || b.StopReason != "" ||
		b.Total != 3 || b.Sent != 0 || b.Failed != 0 || b.Pending != 3 || b.Waiting || b.CreatedAt.IsZero() || !b.FinishedAt.IsZero() {
		t.Errorf("batch = %+v", b)
	}

	ms, err := e.s.BatchMessages(ctx, id)
	if err != nil || len(ms) != 3 {
		t.Fatalf("messages = %+v, %v", ms, err)
	}
	if ms[0].ToName != "Marie Dupont" || ms[0].ToEmail != "marie@example.fr" || ms[0].Status != MsgPending || ms[0].ParentID == 0 {
		t.Errorf("message 0 = %+v", ms[0])
	}
	if ms[0].Subject != "" || ms[0].Body != "" {
		t.Errorf("BatchMessages must leave texts empty: %+v", ms[0])
	}
	if len(ms[0].Children) != 2 || ms[0].Children[0].Name != "Léa Dupont" || ms[0].Children[0].FamilyNumber != "10" ||
		ms[0].Children[1].Name != "Tom Dupont" || ms[0].Children[0].ChildID != e.kids["Léa Dupont"].ID {
		t.Errorf("children = %+v", ms[0].Children)
	}
	if ms[2].ParentID != 0 || len(ms[2].Children) != 0 {
		t.Errorf("message 2 = %+v", ms[2])
	}

	m := e.msg(t, id, ms[0].ID)
	if m.Subject != "Menu de Marie" || m.Body != "Bonjour Marie" {
		t.Errorf("message = %+v", m)
	}
	if m := e.msg(t, id, ms[2].ID); m.Body != "" {
		t.Errorf("empty body = %q", m.Body)
	}
}

func TestCreateBatchChildrenKeepPositionOrder(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	nb := e.batch("T", 1)
	nb.Messages[0].Children = []MessageChild{{Name: "Zed"}, {Name: "Alpha"}, {Name: "Moi"}}
	id, _, err := e.s.CreateBatch(ctx, nb)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := e.s.BatchMessages(ctx, id)
	var got []string
	for _, c := range ms[0].Children {
		got = append(got, c.Name)
		if c.ChildID != 0 {
			t.Errorf("ChildID = %d for a copy without link", c.ChildID)
		}
	}
	if strings.Join(got, ",") != "Zed,Alpha,Moi" {
		t.Errorf("children = %v", got)
	}
}

func TestCreateBatchSameTokenSameBatch(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "ONCE", 2)

	id2, created, err := e.s.CreateBatch(ctx, e.batch("ONCE", 3))
	if err != nil || created || id2 != id {
		t.Fatalf("second = %d, %v, %v", id2, created, err)
	}
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM batches`); n != 1 {
		t.Errorf("batches = %d", n)
	}
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM messages`); n != 2 {
		t.Errorf("messages = %d", n)
	}

	// The token wins over staleness: after a later import, the same token
	// still finds its batch.
	replaceList(t, e.s, e.listID, rowsPaul)
	id3, created, err := e.s.CreateBatch(ctx, e.batch("ONCE", 2))
	if err != nil || created || id3 != id {
		t.Fatalf("after import = %d, %v, %v", id3, created, err)
	}
	// A deleted list too.
	if err := e.s.DeleteList(ctx, e.listID); err != nil {
		t.Fatal(err)
	}
	if id4, created, err := e.s.CreateBatch(ctx, e.batch("ONCE", 2)); err != nil || created || id4 != id {
		t.Fatalf("after delete = %d, %v, %v", id4, created, err)
	}
}

func TestCreateBatchTwoTokensTwoBatches(t *testing.T) {
	e := newBatchEnv(t)
	a, b := e.create(t, "A", 1), e.create(t, "B", 1)
	if a == b {
		t.Fatal("same ID")
	}
}

func TestCreateBatchRefusals(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)

	tests := []struct {
		name  string
		edit  func(*NewBatch)
		stale bool
	}{
		{"stale version", func(b *NewBatch) { b.Version++ }, true},
		{"older version", func(b *NewBatch) { b.Version-- }, true},
		{"unknown list", func(b *NewBatch) { b.ListID = 9999 }, true},
		{"no list", func(b *NewBatch) { b.ListID = 0 }, true},
		{"empty token", func(b *NewBatch) { b.Token = "" }, false},
		{"no messages", func(b *NewBatch) { b.Messages = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := e.batch("X", 2)
			tt.edit(&nb)
			_, created, err := e.s.CreateBatch(ctx, nb)
			if err == nil || created {
				t.Fatalf("err = %v, created = %v", err, created)
			}
			if errors.Is(err, ErrStaleSelection) != tt.stale {
				t.Errorf("ErrStaleSelection = %v, want %v (%v)", !tt.stale, tt.stale, err)
			}
			for _, table := range []string{"batches", "messages", "message_children"} {
				if n := countRows(t, e.s, `SELECT COUNT(*) FROM `+table); n != 0 {
					t.Errorf("%s has %d rows", table, n)
				}
			}
		})
	}

	t.Run("failure in the middle writes nothing", func(t *testing.T) {
		nb := e.batch("MID", 2)
		nb.Messages[1].Children = []MessageChild{{ChildID: 987654, Name: "ghost"}} // foreign key violation
		if _, created, err := e.s.CreateBatch(ctx, nb); err == nil || created {
			t.Fatalf("err = %v, created = %v", err, created)
		}
		if n := countRows(t, e.s, `SELECT COUNT(*) FROM batches`); n != 0 {
			t.Errorf("batches = %d", n)
		}
		if n := countRows(t, e.s, `SELECT COUNT(*) FROM messages`); n != 0 {
			t.Errorf("messages = %d", n)
		}
	})
}

// ---- history survives ---------------------------------------------------------

func TestHistorySurvivesImportRenameAndDelete(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "H", 2)
	mids := e.msgIDs(t, id)

	// Re-import without Marie: her parent and both children are deleted.
	replaceList(t, e.s, e.listID, rowsPaul)
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM parents WHERE email = 'marie@example.fr'`); n != 0 {
		t.Fatal("setup: Marie still there")
	}
	m := e.msg(t, id, mids[0])
	if m.ParentID != 0 || m.ToName != "Marie Dupont" || m.ToEmail != "marie@example.fr" || m.Subject != "Menu de Marie" || m.Body != "Bonjour Marie" {
		t.Errorf("message = %+v", m)
	}
	if len(m.Children) != 2 || m.Children[0].ChildID != 0 || m.Children[0].Name != "Léa Dupont" || m.Children[1].Name != "Tom Dupont" {
		t.Errorf("children = %+v", m.Children)
	}
	if n := e.batchOf(t, id).Total; n != 2 {
		t.Errorf("total = %d", n)
	}

	// A new import reuses IDs: nothing may link to the old message.
	replaceList(t, e.s, e.listID, append(append([]ImportRow{}, rowsPaul...), ImportRow{ParentName: "Nouveau Parent", ChildName: "Nouvel Enfant", Email: "n@example.fr"}))
	for _, mid := range mids {
		m := e.msg(t, id, mid)
		if m.Children[0].ChildID != 0 && m.Children[0].Name == "Léa Dupont" {
			t.Errorf("old message linked to a reused child: %+v", m.Children)
		}
	}
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM message_children WHERE child_name IN ('Nouvel Enfant') OR child_id IN (SELECT id FROM children WHERE name = 'Nouvel Enfant')`); n != 0 {
		t.Errorf("a history row points at the new child")
	}
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM messages WHERE parent_id IN (SELECT id FROM parents WHERE email = 'n@example.fr')`); n != 0 {
		t.Errorf("a history row points at the new parent")
	}

	// Rename keeps the old name in the batch.
	if err := e.s.RenameList(ctx, e.listID, "Autre nom"); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, id); b.ListName != "Cantine CE1" || b.ListID != e.listID {
		t.Errorf("batch after rename = %+v", b)
	}

	// Delete: batch kept, ListID 0.
	if err := e.s.DeleteList(ctx, e.listID); err != nil {
		t.Fatal(err)
	}
	b := e.batchOf(t, id)
	if b.ListID != 0 || b.ListName != "Cantine CE1" || b.Total != 2 {
		t.Errorf("batch after delete = %+v", b)
	}
	if m := e.msg(t, id, mids[1]); m.ToEmail != "paul@example.fr" || m.Body != "Bonjour Paul" {
		t.Errorf("message after delete = %+v", m)
	}
	batches, total, err := e.s.Batches(ctx, 10)
	if err != nil || total != 1 || len(batches) != 1 {
		t.Errorf("batches = %+v, %d, %v", batches, total, err)
	}
	if n := countRows(t, e.s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
}

// ---- queue --------------------------------------------------------------------

func TestClaimOrderAndAttempts(t *testing.T) {
	e := newBatchEnv(t)
	b1 := e.create(t, "B1", 2)
	b2 := e.create(t, "B2", 1)
	m1 := e.msgIDs(t, b1)
	m2 := e.msgIDs(t, b2)

	q := e.claim(t)
	if q.ID != m1[0] || q.BatchID != b1 || q.Attempts != 1 || q.ToEmail != "marie@example.fr" || q.ToName != "Marie Dupont" || q.Subject != "Menu de Marie" || q.Body != "Bonjour Marie" {
		t.Errorf("claim 1 = %+v", q)
	}
	if m := e.msg(t, b1, q.ID); m.Status != MsgSending || m.Attempts != 1 {
		t.Errorf("claimed message = %+v", m)
	}
	if got := e.claim(t); got.ID != m1[1] {
		t.Errorf("claim 2 = %+v", got)
	}
	if got := e.claim(t); got.ID != m2[0] || got.BatchID != b2 {
		t.Errorf("claim 3 = %+v", got)
	}
	e.noClaim(t) // all are sending
}

func TestClaimSkipsStoppedBatches(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	b1 := e.create(t, "B1", 2)
	b2 := e.create(t, "B2", 1)
	if err := e.s.StopBatch(ctx, b1, StopUser); err != nil {
		t.Fatal(err)
	}
	if q := e.claim(t); q.BatchID != b2 {
		t.Errorf("claimed from batch %d, want %d", q.BatchID, b2)
	}
	e.noClaim(t)
	if b := e.batchOf(t, b1); b.Status != BatchStopped || b.Pending != 2 {
		t.Errorf("stopped batch = %+v", b)
	}
}

func TestClaimClosesBatchWithNothingToSend(t *testing.T) {
	e := newBatchEnv(t)
	id := e.create(t, "B", 1)
	mid := e.msgIDs(t, id)[0]
	// Make the message failed behind the queue's back; the batch stays sending.
	if _, err := e.s.db.Exec(`UPDATE messages SET status = 'failed' WHERE id = ?`, mid); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, id); b.Status != BatchSending {
		t.Fatalf("setup: %+v", b)
	}
	e.noClaim(t)
	b := e.batchOf(t, id)
	if b.Status != BatchDone || b.FinishedAt.IsZero() {
		t.Errorf("batch = %+v", b)
	}
}

func TestMarkSentAndFailedClose(t *testing.T) {
	e := newBatchEnv(t)
	ctx := context.Background()
	id := e.create(t, "B", 2)

	q1 := e.claim(t)
	if err := e.s.MarkSent(ctx, q1.ID); err != nil {
		t.Fatal(err)
	}
	m := e.msg(t, id, q1.ID)
	if m.Status != MsgSent || m.SentAt.IsZero() || m.ErrorCode != "" {
		t.Errorf("sent message = %+v", m)
	}
	if b := e.batchOf(t, id); b.Status != BatchSending || b.Sent != 1 || b.Pending != 1 || !b.FinishedAt.IsZero() {
		t.Errorf("batch after first = %+v", b)
	}

	q2 := e.claim(t)
	if err := e.s.MarkFailed(ctx, q2.ID, ErrCodeSend, "boom"); err != nil {
		t.Fatal(err)
	}
	m = e.msg(t, id, q2.ID)
	if m.Status != MsgFailed || m.ErrorCode != ErrCodeSend || m.Error != "boom" || !m.SentAt.IsZero() {
		t.Errorf("failed message = %+v", m)
	}
	b := e.batchOf(t, id)
	if b.Status != BatchDone || b.FinishedAt.IsZero() || b.Sent != 1 || b.Failed != 1 || b.Pending != 0 {
		t.Errorf("batch after last = %+v", b)
	}
}

func TestMarkOnlyChangesSendingMessages(t *testing.T) {
	e := newBatchEnv(t)
	ctx := context.Background()
	id := e.create(t, "B", 1)
	mid := e.msgIDs(t, id)[0]

	// Pending message: nothing changes.
	for name, f := range map[string]func() error{
		"MarkSent":       func() error { return e.s.MarkSent(ctx, mid) },
		"MarkFailed":     func() error { return e.s.MarkFailed(ctx, mid, ErrCodeSend, "x") },
		"ReleaseMessage": func() error { return e.s.ReleaseMessage(ctx, mid) },
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on pending = %v", name, err)
		}
	}
	if m := e.msg(t, id, mid); m.Status != MsgPending {
		t.Errorf("message = %+v", m)
	}

	q := e.claim(t)
	if err := e.s.MarkSent(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	// Already sent: a second mark must not turn it into failed.
	if err := e.s.MarkFailed(ctx, q.ID, ErrCodeSend, "late"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkFailed on sent = %v", err)
	}
	if err := e.s.MarkSent(ctx, 98765); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkSent unknown = %v", err)
	}
	if m := e.msg(t, id, mid); m.Status != MsgSent {
		t.Errorf("message = %+v", m)
	}
}

func TestMarkFailedCleansAndCutsDetail(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, in, want string
	}{
		{"control characters become spaces", "line1\r\nline2\ttab\x00nul\x1b[0m", "line1  line2 tab nul [0m"},
		{"invalid utf-8", "bad \xff\xfe bytes", "bad ? bytes"},
		{"500 runes kept", strings.Repeat("é", 500), strings.Repeat("é", 500)},
		{"501 runes cut", strings.Repeat("é", 501), strings.Repeat("é", 500)},
		{"1000 ascii cut", strings.Repeat("a", 1000), strings.Repeat("a", 500)},
		{"empty", "", ""},
		{"html is kept as text", "<b>x</b> & y", "<b>x</b> & y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newBatchEnv(t)
			id := e.create(t, "B", 1)
			q := e.claim(t)
			if err := e.s.MarkFailed(ctx, q.ID, ErrCodeSend, tt.in); err != nil {
				t.Fatal(err)
			}
			if got := e.msg(t, id, q.ID).Error; got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReleaseMessage(t *testing.T) {
	e := newBatchEnv(t)
	ctx := context.Background()
	id := e.create(t, "B", 1)
	q := e.claim(t)
	if err := e.s.ReleaseMessage(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if m := e.msg(t, id, q.ID); m.Status != MsgPending || m.Attempts != 1 {
		t.Errorf("message = %+v", m)
	}
	again := e.claim(t)
	if again.ID != q.ID || again.Attempts != 2 {
		t.Errorf("claim again = %+v", again)
	}
}

func TestStopWhileSendingThenMarkSent(t *testing.T) {
	ctx := context.Background()
	t.Run("last message: batch done", func(t *testing.T) {
		e := newBatchEnv(t)
		id := e.create(t, "B", 1)
		q := e.claim(t)
		if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MarkSent(ctx, q.ID); err != nil {
			t.Fatal(err)
		}
		if b := e.batchOf(t, id); b.Status != BatchDone || b.StopReason != "" || b.FinishedAt.IsZero() {
			t.Errorf("batch = %+v", b)
		}
	})
	t.Run("more pending: stays stopped, next not claimed", func(t *testing.T) {
		e := newBatchEnv(t)
		id := e.create(t, "B", 2)
		q := e.claim(t)
		if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MarkSent(ctx, q.ID); err != nil {
			t.Fatal(err)
		}
		b := e.batchOf(t, id)
		if b.Status != BatchStopped || b.StopReason != StopUser || b.Sent != 1 || b.Pending != 1 {
			t.Errorf("batch = %+v", b)
		}
		e.noClaim(t)
	})
}

func TestStopBatch(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "B", 2)

	if err := e.s.StopBatch(ctx, id, StopAccount); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, id); b.Status != BatchStopped || b.StopReason != StopAccount {
		t.Errorf("batch = %+v", b)
	}
	// Stopped: left as is (reason not overwritten).
	if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, id); b.StopReason != StopAccount {
		t.Errorf("reason = %q", b.StopReason)
	}
	// Done: untouched.
	done := e.create(t, "D", 1)
	q := e.claim(t) // claims from batch D (B is stopped)
	if q.BatchID != done {
		t.Fatalf("claimed batch %d", q.BatchID)
	}
	if err := e.s.MarkSent(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.StopBatch(ctx, done, StopUser); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, done); b.Status != BatchDone || b.StopReason != "" {
		t.Errorf("done batch = %+v", b)
	}
	if err := e.s.StopBatch(ctx, 9999, StopUser); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
}

func TestResumeBatch(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "B", 2)

	// Sending: not resumable.
	if ok, err := e.s.ResumeBatch(ctx, id); err != nil || ok {
		t.Errorf("resume sending = %v, %v", ok, err)
	}
	if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
		t.Fatal(err)
	}
	ok, err := e.s.ResumeBatch(ctx, id)
	if err != nil || !ok {
		t.Fatalf("resume = %v, %v", ok, err)
	}
	if b := e.batchOf(t, id); b.Status != BatchSending || b.StopReason != "" || !b.FinishedAt.IsZero() {
		t.Errorf("batch = %+v", b)
	}
	if q := e.claim(t); q.BatchID != id {
		t.Errorf("claim = %+v", q)
	}

	t.Run("nothing pending", func(t *testing.T) {
		e := newBatchEnv(t)
		id := e.create(t, "B", 1)
		q := e.claim(t)
		if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
			t.Fatal(err)
		}
		// Message still sending: not pending either.
		if ok, err := e.s.ResumeBatch(ctx, id); err != nil || ok {
			t.Errorf("resume = %v, %v", ok, err)
		}
		if err := e.s.MarkFailed(ctx, q.ID, ErrCodeSend, "x"); err != nil {
			t.Fatal(err)
		}
		if ok, err := e.s.ResumeBatch(ctx, id); err != nil || ok {
			t.Errorf("resume done = %v, %v", ok, err)
		}
		if b := e.batchOf(t, id); b.Status != BatchDone {
			t.Errorf("batch = %+v", b)
		}
	})
	if _, err := e.s.ResumeBatch(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
}

func TestRetryFailed(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "B", 3)

	q1 := e.claim(t) // fails
	if err := e.s.MarkFailed(ctx, q1.ID, ErrCodeSend, "refused"); err != nil {
		t.Fatal(err)
	}
	q2 := e.claim(t) // sent
	if err := e.s.MarkSent(ctx, q2.ID); err != nil {
		t.Fatal(err)
	}
	q3 := e.claim(t) // fails with attempts 2
	if err := e.s.ReleaseMessage(ctx, q3.ID); err != nil {
		t.Fatal(err)
	}
	q3 = e.claim(t)
	if q3.Attempts != 2 {
		t.Fatalf("attempts = %d", q3.Attempts)
	}
	if err := e.s.MarkFailed(ctx, q3.ID, ErrCodeRateLimited, "slow"); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, id); b.Status != BatchDone || b.Failed != 2 {
		t.Fatalf("setup batch = %+v", b)
	}
	before := e.msg(t, id, q1.ID)

	n, err := e.s.RetryFailed(ctx, id)
	if err != nil || n != 2 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	b := e.batchOf(t, id)
	if b.Status != BatchSending || b.StopReason != "" || !b.FinishedAt.IsZero() || b.Pending != 2 || b.Sent != 1 || b.Failed != 0 {
		t.Errorf("batch = %+v", b)
	}
	m := e.msg(t, id, q1.ID)
	if m.Status != MsgPending || m.ErrorCode != "" || m.Error != "" || m.Attempts != 0 {
		t.Errorf("retried message = %+v", m)
	}
	if m.Subject != before.Subject || m.Body != before.Body || m.ToEmail != before.ToEmail || m.ToName != before.ToName {
		t.Errorf("content changed: %+v vs %+v", m, before)
	}
	if m := e.msg(t, id, q2.ID); m.Status != MsgSent {
		t.Errorf("sent message touched: %+v", m)
	}
	if m := e.msg(t, id, q3.ID); m.Attempts != 0 {
		t.Errorf("attempts not reset: %+v", m)
	}

	// Second call: nothing changes.
	if n, err := e.s.RetryFailed(ctx, id); err != nil || n != 0 {
		t.Errorf("second retry = %d, %v", n, err)
	}
	if _, err := e.s.RetryFailed(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
}

func TestRetryFailedOnStoppedBatchResumesIt(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "B", 2)
	q := e.claim(t)
	if err := e.s.MarkFailed(ctx, q.ID, ErrCodeSend, "x"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.RetryFailed(ctx, id); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	if b := e.batchOf(t, id); b.Status != BatchSending || b.StopReason != "" {
		t.Errorf("batch = %+v", b)
	}
}

func TestRetryNothingFailedChangesNothing(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	id := e.create(t, "B", 1)
	if err := e.s.StopBatch(ctx, id, StopUser); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.RetryFailed(ctx, id); err != nil || n != 0 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	if b := e.batchOf(t, id); b.Status != BatchStopped || b.StopReason != StopUser {
		t.Errorf("batch = %+v", b)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)

	crashed := e.create(t, "CRASHED", 2) // one message sending, one pending
	q := e.claim(t)
	lastOne := e.create(t, "LAST", 1) // its only message is sending
	for {
		c, ok, err := e.s.ClaimNextMessage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		_ = c
	}
	// Put one message of "crashed" back to pending to have a pending one.
	pendingMsg := e.msgIDs(t, crashed)[1]
	if _, err := e.s.db.Exec(`UPDATE messages SET status = 'pending' WHERE id = ?`, pendingMsg); err != nil {
		t.Fatal(err)
	}
	// A sending batch with nothing pending and nothing sending.
	empty := e.create(t, "EMPTY", 1)
	if _, err := e.s.db.Exec(`UPDATE messages SET status = 'sent' WHERE batch_id = ?`, empty); err != nil {
		t.Fatal(err)
	}
	stopped := e.create(t, "STOPPED", 1)
	if err := e.s.StopBatch(ctx, stopped, StopUser); err != nil {
		t.Fatal(err)
	}
	done := e.create(t, "DONE", 1)
	if _, err := e.s.db.Exec(`UPDATE messages SET status = 'sent' WHERE batch_id = ?`, done); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`UPDATE batches SET status = 'done' WHERE id = ?`, done); err != nil {
		t.Fatal(err)
	}

	if err := e.s.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}

	if m := e.msg(t, crashed, q.ID); m.Status != MsgFailed || m.ErrorCode != ErrCodeUnknown {
		t.Errorf("interrupted message = %+v", m)
	}
	if b := e.batchOf(t, crashed); b.Status != BatchStopped || b.StopReason != StopInterrupted || b.Pending != 1 || b.Failed != 1 {
		t.Errorf("crashed batch = %+v", b)
	}
	if m := e.msg(t, crashed, pendingMsg); m.Status != MsgPending {
		t.Errorf("pending message resent or changed: %+v", m)
	}
	// Batch whose only message was sending: nothing pending, so done (with a failed message).
	if b := e.batchOf(t, lastOne); b.Status != BatchDone || b.Failed != 1 || b.FinishedAt.IsZero() {
		t.Errorf("last batch = %+v", b)
	}
	if b := e.batchOf(t, empty); b.Status != BatchDone {
		t.Errorf("empty batch = %+v", b)
	}
	if b := e.batchOf(t, stopped); b.Status != BatchStopped || b.StopReason != StopUser {
		t.Errorf("stopped batch = %+v", b)
	}
	if b := e.batchOf(t, done); b.Status != BatchDone {
		t.Errorf("done batch = %+v", b)
	}
	if n, err := e.s.InterruptedBatches(ctx); err != nil || n != 1 {
		t.Errorf("interrupted = %d, %v", n, err)
	}
	// No automatic resume: nothing can be claimed.
	e.noClaim(t)

	// Idempotent.
	before := fmt.Sprint(e.batchOf(t, crashed), e.batchOf(t, lastOne))
	if err := e.s.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if after := fmt.Sprint(e.batchOf(t, crashed), e.batchOf(t, lastOne)); after != before {
		t.Errorf("second recover changed state:\n%s\n%s", before, after)
	}
}

// ---- reads --------------------------------------------------------------------

func TestBatchesOrderLimitTotal(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	var ids []int64
	for i := range 5 {
		ids = append(ids, e.create(t, fmt.Sprintf("T%d", i), 1))
	}
	got, total, err := e.s.Batches(ctx, 3)
	if err != nil || total != 5 || len(got) != 3 {
		t.Fatalf("batches = %d, total %d, %v", len(got), total, err)
	}
	for i, b := range got {
		if b.ID != ids[4-i] {
			t.Errorf("batch %d = %d, want %d", i, b.ID, ids[4-i])
		}
	}
	empty := openTest(t)
	if got, total, err := empty.Batches(ctx, 10); err != nil || total != 0 || got == nil || len(got) != 0 {
		t.Errorf("empty = %v, %d, %v", got, total, err)
	}
}

func TestBatchCountsAndWaiting(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	first := e.create(t, "F", 2)
	second := e.create(t, "S", 1)

	if b := e.batchOf(t, first); b.Waiting {
		t.Error("oldest batch must not wait")
	}
	if b := e.batchOf(t, second); !b.Waiting {
		t.Error("second batch must wait")
	}
	q := e.claim(t) // sending counts as pending
	if b := e.batchOf(t, first); b.Pending != 2 || b.Total != 2 {
		t.Errorf("first = %+v", b)
	}
	if err := e.s.StopBatch(ctx, first, StopUser); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, second); b.Waiting {
		t.Error("second batch waits behind a stopped batch")
	}
	if b := e.batchOf(t, first); b.Waiting {
		t.Error("stopped batch never waits")
	}
	if err := e.s.MarkSent(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ResumeBatch(ctx, first); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, second); !b.Waiting {
		t.Error("second batch waits again")
	}
	// The older batch has nothing left: the newer one no longer waits.
	q = e.claim(t)
	if err := e.s.MarkSent(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if b := e.batchOf(t, first); b.Status != BatchDone {
		t.Fatalf("first = %+v", b)
	}
	if b := e.batchOf(t, second); b.Waiting {
		t.Error("second batch still waiting")
	}
	if _, err := e.s.Batch(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
}

func TestMessageWrongBatchIsNotFound(t *testing.T) {
	e := newBatchEnv(t)
	a := e.create(t, "A", 1)
	b := e.create(t, "B", 1)
	mid := e.msgIDs(t, a)[0]
	if _, err := e.s.Message(context.Background(), b, mid); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := e.s.Message(context.Background(), a, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestBatchMessagesEmptyForUnknownBatch(t *testing.T) {
	e := newBatchEnv(t)
	ms, err := e.s.BatchMessages(context.Background(), 4242)
	if err != nil || len(ms) != 0 {
		t.Errorf("messages = %v, %v", ms, err)
	}
}

func TestInterruptedBatchesCountsOnlyInterrupted(t *testing.T) {
	ctx := context.Background()
	e := newBatchEnv(t)
	a := e.create(t, "A", 1)
	b := e.create(t, "B", 1)
	if n, _ := e.s.InterruptedBatches(ctx); n != 0 {
		t.Fatalf("n = %d", n)
	}
	if err := e.s.StopBatch(ctx, a, StopUser); err != nil {
		t.Fatal(err)
	}
	if err := e.s.StopBatch(ctx, b, StopAccount); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.s.InterruptedBatches(ctx); n != 0 {
		t.Errorf("user/account stops counted: %d", n)
	}
	if err := e.s.StopBatch(ctx, e.create(t, "C", 1), StopInterrupted); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.InterruptedBatches(ctx); err != nil || n != 1 {
		t.Errorf("n = %d, %v", n, err)
	}
}

func TestBatchIDsAreNeverReused(t *testing.T) {
	e := newBatchEnv(t)
	a := e.create(t, "A", 1)
	if _, err := e.s.db.Exec(`DELETE FROM batches WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if b := e.create(t, "B", 1); b <= a {
		t.Errorf("ID %d reused after %d", b, a)
	}
}
