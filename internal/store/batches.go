package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Batch and message statuses, stop reasons and error codes (SPEC §4.4).
const (
	BatchSending = "sending" // the queue sends its pending messages
	BatchStopped = "stopped" // pending messages wait for "resume"
	BatchDone    = "done"    // no pending message left

	StopUser        = "user"
	StopInterrupted = "interrupted" // app closed or crashed during the sending
	StopAccount     = "account"     // the sender cannot send at all

	MsgPending = "pending"
	MsgSending = "sending" // claimed by the queue, being sent
	MsgSent    = "sent"
	MsgFailed  = "failed"

	ErrCodeSend        = "send"
	ErrCodeRateLimited = "rate_limited"
	ErrCodeAccount     = "account"
	ErrCodeUnknown     = "unknown" // maybe sent: the app stopped, or the provider did not answer
)

// maxErrorLen is the longest provider error text kept, in runes.
const maxErrorLen = 500

// NewBatch is a sending to store (see CreateBatch).
type NewBatch struct {
	Token         string // one-time token of the confirmation page
	ListID        int64
	Version       int64 // list version of the selection
	Families      bool
	Provider      string
	Subject, Body string // as typed, normalized
	Messages      []NewMessage
}

// NewMessage is one email of a NewBatch, with its final rendered text.
type NewMessage struct {
	ParentID                       int64
	ToName, ToEmail, Subject, Body string
	Children                       []MessageChild
}

// MessageChild is a child or family entry an email is about. Names are
// copies: they stay when the entry is deleted.
type MessageChild struct {
	ChildID      int64 // 0 once the entry is deleted
	Name         string
	FamilyNumber string
}

// Batch is one sending with its counts.
type Batch struct {
	ID                           int64
	ListID                       int64  // 0 if the list was deleted
	ListName                     string // copy at sending time
	Families                     bool
	Provider                     string
	Subject, Body                string
	Status, StopReason           string
	CreatedAt, FinishedAt        time.Time
	Total, Sent, Failed, Pending int  // Pending counts pending + sending messages
	Waiting                      bool // sending, but an older batch is sending first
}

// Message is one email of a batch as stored.
type Message struct {
	ID, BatchID, ParentID          int64
	ToName, ToEmail, Subject, Body string
	Status, ErrorCode, Error       string
	Attempts                       int
	SentAt                         time.Time
	Children                       []MessageChild
}

// QueuedMessage is a message claimed by the queue.
type QueuedMessage struct {
	ID, BatchID                    int64
	ToName, ToEmail, Subject, Body string
	Provider                       string // of the batch (batches.provider)
	Attempts                       int    // including this claim
}

// CreateBatch stores a batch and its messages as pending in one
// transaction. If a batch already has b.Token it returns that batch's ID
// with created=false and writes nothing (the token is checked before the
// list version). Otherwise the list version is checked (ErrStaleSelection if
// the list is gone or changed) and the list name is copied.
func (s *Store) CreateBatch(ctx context.Context, b NewBatch) (id int64, created bool, err error) {
	if b.Token == "" {
		return 0, false, errors.New("create batch: empty token")
	}
	if len(b.Messages) == 0 {
		return 0, false, errors.New("create batch: no messages")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `SELECT id FROM batches WHERE token = ?`, b.Token).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("find batch by token: %w", err)
	}

	current, err := listVersion(ctx, tx, b.ListID)
	if errors.Is(err, ErrNotFound) {
		return 0, false, ErrStaleSelection
	}
	if err != nil {
		return 0, false, err
	}
	if current != b.Version {
		return 0, false, ErrStaleSelection
	}
	var listName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM lists WHERE id = ?`, b.ListID).Scan(&listName); err != nil {
		return 0, false, fmt.Errorf("read list name: %w", err)
	}

	now := s.timestamp()
	res, err := tx.ExecContext(ctx, `INSERT INTO batches
		(token, list_id, list_name, list_version, families, provider, subject, body, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Token, b.ListID, listName, b.Version, b.Families, b.Provider, b.Subject, b.Body, BatchSending, now)
	if err != nil {
		return 0, false, fmt.Errorf("insert batch: %w", err)
	}
	if id, err = res.LastInsertId(); err != nil {
		return 0, false, err
	}

	insMsg, err := tx.PrepareContext(ctx, `INSERT INTO messages
		(batch_id, parent_id, to_name, to_email, subject, body, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, false, err
	}
	defer insMsg.Close()
	insChild, err := tx.PrepareContext(ctx, `INSERT INTO message_children
		(message_id, position, child_id, child_name, family_number) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, false, err
	}
	defer insChild.Close()

	for _, m := range b.Messages {
		res, err := insMsg.ExecContext(ctx, id, nullID(m.ParentID), m.ToName, m.ToEmail, m.Subject, m.Body, MsgPending, now)
		if err != nil {
			return 0, false, fmt.Errorf("insert message: %w", err)
		}
		mid, err := res.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		for pos, c := range m.Children {
			if _, err := insChild.ExecContext(ctx, mid, pos, nullID(c.ChildID), c.Name, c.FamilyNumber); err != nil {
				return 0, false, fmt.Errorf("insert message child: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit batch: %w", err)
	}
	return id, true, nil
}

// nullID maps the 0 of "no row" to NULL.
func nullID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}

// batchSelect reads batches with their counts. The data is small, so
// correlated subqueries are enough.
const batchSelect = `
SELECT b.id, COALESCE(b.list_id, 0), b.list_name, b.families, b.provider, b.subject, b.body,
  b.status, b.stop_reason, b.created_at, b.finished_at,
  (SELECT COUNT(*) FROM messages m WHERE m.batch_id = b.id),
  (SELECT COUNT(*) FROM messages m WHERE m.batch_id = b.id AND m.status = 'sent'),
  (SELECT COUNT(*) FROM messages m WHERE m.batch_id = b.id AND m.status = 'failed'),
  (SELECT COUNT(*) FROM messages m WHERE m.batch_id = b.id AND m.status IN ('pending', 'sending')),
  b.status = 'sending' AND EXISTS (
    SELECT 1 FROM batches o WHERE o.id < b.id AND o.status = 'sending' AND EXISTS (
      SELECT 1 FROM messages m WHERE m.batch_id = o.id AND m.status IN ('pending', 'sending')))
FROM batches b`

func scanBatch(r rowScanner) (Batch, error) {
	var b Batch
	var created, finished string
	if err := r.Scan(&b.ID, &b.ListID, &b.ListName, &b.Families, &b.Provider, &b.Subject, &b.Body,
		&b.Status, &b.StopReason, &created, &finished,
		&b.Total, &b.Sent, &b.Failed, &b.Pending, &b.Waiting); err != nil {
		return Batch{}, err
	}
	b.CreatedAt, b.FinishedAt = parseTime(created), parseTime(finished)
	return b, nil
}

// Batches returns the newest batches first, at most limit, and the total
// number of batches.
func (s *Store) Batches(ctx context.Context, limit int) ([]Batch, int, error) {
	rows, err := s.db.QueryContext(ctx, batchSelect+` ORDER BY b.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("query batches: %w", err)
	}
	defer rows.Close()

	out := []Batch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan batch: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("query batches: %w", err)
	}
	rows.Close() // one connection: free it before the next query

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count batches: %w", err)
	}
	return out, total, nil
}

// Batch returns one batch, or ErrNotFound.
func (s *Store) Batch(ctx context.Context, id int64) (Batch, error) {
	b, err := scanBatch(s.db.QueryRowContext(ctx, batchSelect+` WHERE b.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	if err != nil {
		return Batch{}, fmt.Errorf("read batch: %w", err)
	}
	return b, nil
}

// BatchMessages returns the messages of a batch in ID order, with their
// children in position order. Subject and body are left empty (they can be
// long): read one message with Message.
func (s *Store) BatchMessages(ctx context.Context, batchID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch_id, COALESCE(parent_id, 0), to_name, to_email,
		status, error_code, error, attempts, sent_at
		FROM messages WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	out := []Message{}
	index := map[int64]int{}
	for rows.Next() {
		var m Message
		var sent string
		if err := rows.Scan(&m.ID, &m.BatchID, &m.ParentID, &m.ToName, &m.ToEmail,
			&m.Status, &m.ErrorCode, &m.Error, &m.Attempts, &sent); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.SentAt = parseTime(sent)
		index[m.ID] = len(out)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	rows.Close() // one connection: free it before the next query

	crows, err := s.db.QueryContext(ctx, `SELECT mc.message_id, COALESCE(mc.child_id, 0), mc.child_name, mc.family_number
		FROM message_children mc JOIN messages m ON m.id = mc.message_id
		WHERE m.batch_id = ? ORDER BY mc.message_id, mc.position`, batchID)
	if err != nil {
		return nil, fmt.Errorf("query message children: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var mid int64
		var c MessageChild
		if err := crows.Scan(&mid, &c.ChildID, &c.Name, &c.FamilyNumber); err != nil {
			return nil, fmt.Errorf("scan message child: %w", err)
		}
		if i, ok := index[mid]; ok {
			out[i].Children = append(out[i].Children, c)
		}
	}
	if err := crows.Err(); err != nil {
		return nil, fmt.Errorf("query message children: %w", err)
	}
	return out, nil
}

// Message returns one message of a batch with its text, or ErrNotFound (also
// when the message belongs to another batch).
func (s *Store) Message(ctx context.Context, batchID, id int64) (Message, error) {
	var m Message
	var sent string
	err := s.db.QueryRowContext(ctx, `SELECT id, batch_id, COALESCE(parent_id, 0), to_name, to_email,
		subject, body, status, error_code, error, attempts, sent_at
		FROM messages WHERE batch_id = ? AND id = ?`, batchID, id).
		Scan(&m.ID, &m.BatchID, &m.ParentID, &m.ToName, &m.ToEmail,
			&m.Subject, &m.Body, &m.Status, &m.ErrorCode, &m.Error, &m.Attempts, &sent)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("read message: %w", err)
	}
	m.SentAt = parseTime(sent)

	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(child_id, 0), child_name, family_number
		FROM message_children WHERE message_id = ? ORDER BY position`, id)
	if err != nil {
		return Message{}, fmt.Errorf("query message children: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c MessageChild
		if err := rows.Scan(&c.ChildID, &c.Name, &c.FamilyNumber); err != nil {
			return Message{}, fmt.Errorf("scan message child: %w", err)
		}
		m.Children = append(m.Children, c)
	}
	if err := rows.Err(); err != nil {
		return Message{}, fmt.Errorf("query message children: %w", err)
	}
	return m, nil
}

// InterruptedBatches counts the batches stopped because the app was closed
// during their sending.
func (s *Store) InterruptedBatches(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM batches WHERE status = ? AND stop_reason = ?`, BatchStopped, StopInterrupted).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count interrupted batches: %w", err)
	}
	return n, nil
}

// closeFinishedBatches sets done the sending batches (and the stopped ones
// if withStopped) that have no pending or sending message, so a batch never
// stays open for nothing. batchID limits it to one batch (0: all).
func closeFinishedBatches(ctx context.Context, tx *sql.Tx, batchID int64, withStopped bool, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE batches SET status = ?, stop_reason = '', finished_at = ?
		WHERE (status = 'sending' OR (status = 'stopped' AND ?))
		AND (? = 0 OR id = ?)
		AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.batch_id = batches.id AND m.status IN ('pending', 'sending'))`,
		BatchDone, now, withStopped, batchID, batchID)
	if err != nil {
		return fmt.Errorf("close batch: %w", err)
	}
	return nil
}

// ClaimNextMessage takes the next pending message of a sending batch (lowest
// batch ID, then message ID), sets it to sending and counts the attempt.
// ok is false when there is nothing to send. Batches with nothing left to
// send are closed on the way.
func (s *Store) ClaimNextMessage(ctx context.Context) (q QueuedMessage, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return q, false, err
	}
	defer tx.Rollback()

	now := s.timestamp()
	if err := closeFinishedBatches(ctx, tx, 0, false, now); err != nil {
		return q, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT m.id, m.batch_id, m.to_name, m.to_email, m.subject, m.body, b.provider, m.attempts
		FROM messages m JOIN batches b ON b.id = m.batch_id
		WHERE m.status = ? AND b.status = ?
		ORDER BY m.batch_id, m.id LIMIT 1`, MsgPending, BatchSending).
		Scan(&q.ID, &q.BatchID, &q.ToName, &q.ToEmail, &q.Subject, &q.Body, &q.Provider, &q.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return QueuedMessage{}, false, tx.Commit()
	}
	if err != nil {
		return QueuedMessage{}, false, fmt.Errorf("claim message: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET status = ?, attempts = attempts + 1, updated_at = ? WHERE id = ?`,
		MsgSending, now, q.ID); err != nil {
		return QueuedMessage{}, false, fmt.Errorf("claim message: %w", err)
	}
	q.Attempts++
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, false, fmt.Errorf("commit claim: %w", err)
	}
	return q, true, nil
}

// MarkSent records that a claimed message was sent. The batch becomes done
// when nothing is left to send. Only a message in sending is updated
// (ErrNotFound otherwise).
func (s *Store) MarkSent(ctx context.Context, id int64) error {
	return s.finishMessage(ctx, id, MsgSent, "", "")
}

// MarkFailed records that a claimed message failed, with an error code and
// the provider's text (cleaned and cut). Same rules as MarkSent.
func (s *Store) MarkFailed(ctx context.Context, id int64, code, detail string) error {
	return s.finishMessage(ctx, id, MsgFailed, code, cleanDetail(detail))
}

func (s *Store) finishMessage(ctx context.Context, id int64, status, code, detail string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := s.timestamp()
	sentAt := ""
	if status == MsgSent {
		sentAt = now
	}
	res, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, error_code = ?, error = ?, sent_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, status, code, detail, sentAt, now, id, MsgSending)
	if err != nil {
		return fmt.Errorf("update message: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	var batchID int64
	if err := tx.QueryRowContext(ctx, `SELECT batch_id FROM messages WHERE id = ?`, id).Scan(&batchID); err != nil {
		return fmt.Errorf("read message batch: %w", err)
	}
	if err := closeFinishedBatches(ctx, tx, batchID, true, now); err != nil {
		return err
	}
	return tx.Commit()
}

// cleanDetail makes a provider error text safe to store: valid UTF-8,
// control characters replaced by spaces, cut to maxErrorLen runes. It is the
// only way a provider text reaches the database.
func cleanDetail(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > maxErrorLen {
		s = string([]rune(s)[:maxErrorLen])
	}
	return s
}

// ReleaseMessage puts a message claimed by the queue back to pending: it
// was not sent. Only a message in sending changes (ErrNotFound otherwise).
func (s *Store) ReleaseMessage(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		MsgPending, s.timestamp(), id, MsgSending)
	if err != nil {
		return fmt.Errorf("release message: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// batchStatus reads the status of a batch inside tx (ErrNotFound).
func batchStatus(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM batches WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read batch: %w", err)
	}
	return status, nil
}

// StopBatch stops a sending batch for reason (StopUser, StopAccount). A
// batch that is stopped or done is left as it is (ErrNotFound if unknown).
// The email in flight finishes and is recorded.
func (s *Store) StopBatch(ctx context.Context, id int64, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	status, err := batchStatus(ctx, tx, id)
	if err != nil {
		return err
	}
	if status != BatchSending {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE batches SET status = ?, stop_reason = ? WHERE id = ?`,
		BatchStopped, reason, id); err != nil {
		return fmt.Errorf("stop batch: %w", err)
	}
	return tx.Commit()
}

// ResumeBatch sets a stopped batch back to sending. It returns false, and
// changes nothing, if the batch is not stopped or has no pending message
// (ErrNotFound if unknown).
func (s *Store) ResumeBatch(ctx context.Context, id int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	status, err := batchStatus(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if status != BatchStopped {
		return false, nil
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE batch_id = ? AND status = ?`,
		id, MsgPending).Scan(&pending); err != nil {
		return false, fmt.Errorf("count pending: %w", err)
	}
	if pending == 0 {
		return false, nil
	}
	if err := reopenBatch(ctx, tx, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// reopenBatch sets a batch to sending and clears what described its end.
func reopenBatch(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE batches SET status = ?, stop_reason = '', finished_at = '' WHERE id = ?`,
		BatchSending, id); err != nil {
		return fmt.Errorf("reopen batch: %w", err)
	}
	return nil
}

// RetryFailed puts every failed message of a batch back to pending (same
// text and address, error cleared, attempts reset) and sets the batch to
// sending. It returns the number of messages, 0 if none failed and then
// nothing changes (ErrNotFound if unknown).
func (s *Store) RetryFailed(ctx context.Context, id int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := batchStatus(ctx, tx, id); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, error_code = '', error = '', attempts = 0, updated_at = ?
		WHERE batch_id = ? AND status = ?`, MsgPending, s.timestamp(), id, MsgFailed)
	if err != nil {
		return 0, fmt.Errorf("retry messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if err := reopenBatch(ctx, tx, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit retry: %w", err)
	}
	return int(n), nil
}

// RecoverInterrupted settles what a crash or a close left behind (SPEC
// §4.4), once at start: a message still sending (maybe delivered, maybe
// not) becomes failed with the code "unknown"; a sending batch with pending
// messages becomes stopped (interrupted), one with nothing pending done.
// Nothing is ever sent again by it.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := s.timestamp()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, error_code = ?, error = '', updated_at = ?
		WHERE status = ?`, MsgFailed, ErrCodeUnknown, now, MsgSending); err != nil {
		return fmt.Errorf("recover messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE batches SET status = ?, stop_reason = ?
		WHERE status = ? AND EXISTS (SELECT 1 FROM messages m WHERE m.batch_id = batches.id AND m.status = ?)`,
		BatchStopped, StopInterrupted, BatchSending, MsgPending); err != nil {
		return fmt.Errorf("recover batches: %w", err)
	}
	if err := closeFinishedBatches(ctx, tx, 0, false, now); err != nil {
		return err
	}
	return tx.Commit()
}
