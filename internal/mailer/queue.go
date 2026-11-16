package mailer

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"time"

	"cafeteria-management/internal/store"
)

const (
	// maxAttempts is the number of claims after which a rate-limited email
	// fails instead of waiting again.
	maxAttempts = 5
	// defaultRetryAfter is the wait when the provider gives none.
	defaultRetryAfter = 60 * time.Second
	maxRetryAfter     = 15 * time.Minute
	// errorWait is the wait after a database error, so the loop never spins.
	errorWait = 5 * time.Second
	// defaultSendTimeout bounds one email and the recording of its result.
	defaultSendTimeout = 60 * time.Second
)

// Store is what the queue needs from the database (satisfied by *store.Store).
type Store interface {
	ClaimNextMessage(ctx context.Context) (store.QueuedMessage, bool, error)
	MarkSent(ctx context.Context, id int64) error
	MarkFailed(ctx context.Context, id int64, code, detail string) error
	ReleaseMessage(ctx context.Context, id int64) error
	StopBatch(ctx context.Context, id int64, reason string) error
	RecoverInterrupted(ctx context.Context) error
}

// Options configure a Queue.
type Options struct {
	Provider    string        // recorded on new batches
	Interval    time.Duration // between two sends
	SendTimeout time.Duration // per email; 60 s if 0
}

// Queue sends the pending messages of the database, one at a time. The
// database is the queue: nothing is kept in memory but a wake-up signal.
type Queue struct {
	st     Store
	sender Sender
	opts   Options
	wake   chan struct{}
	// after returns a channel that fires after d; tests replace it.
	after func(d time.Duration) <-chan time.Time
}

// NewQueue builds a queue. Run it with Run, in a goroutine owned by the
// caller.
func NewQueue(st Store, s Sender, o Options) *Queue {
	if o.SendTimeout <= 0 {
		o.SendTimeout = defaultSendTimeout
	}
	return &Queue{st: st, sender: s, opts: o, wake: make(chan struct{}, 1), after: time.After}
}

// Provider returns the provider name to record on new batches.
func (q *Queue) Provider() string { return q.opts.Provider }

// Interval returns the wait between two sends.
func (q *Queue) Interval() time.Duration { return q.opts.Interval }

// Wake tells the queue that there may be something to send. It never blocks.
func (q *Queue) Wake() {
	select {
	case q.wake <- struct{}{}:
	default: // a wake-up is already waiting
	}
}

// Recover settles what a crash or a close left behind (see
// store.RecoverInterrupted). Call it once before serving requests.
func (q *Queue) Recover(ctx context.Context) error {
	return q.st.RecoverInterrupted(ctx)
}

// Run sends until ctx ends. The email in flight when ctx ends finishes and
// is recorded; the rest stays pending. It returns when done: its owner
// waits for it before closing the database.
func (q *Queue) Run(ctx context.Context) {
	for {
		worked, wait, err := q.step(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("queue step failed", "err", err)
		}
		switch {
		case wait > 0:
			// Interval and rate-limit waits end only on their timer or on
			// ctx: a wake-up must not shorten them.
			select {
			case <-ctx.Done():
				return
			case <-q.after(wait):
			}
		case !worked && err == nil:
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
		}
	}
}

// step claims and sends one message and records the result. worked tells
// that a message was claimed. wait is how long to wait before the next step:
// 0 means go on at once if worked, else idle until Wake. A database error is
// returned, with a wait so the loop does not spin.
func (q *Queue) step(ctx context.Context) (worked bool, wait time.Duration, err error) {
	if ctx.Err() != nil { // nothing is claimed after the end
		return false, 0, nil
	}
	msg, ok, err := q.st.ClaimNextMessage(ctx)
	if err != nil {
		return false, errorWait, err
	}
	if !ok {
		return false, 0, nil
	}

	// Once claimed, the email and its result outlive ctx (graceful stop).
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), q.opts.SendTimeout)
	defer cancel()
	sendErr := q.sender.Send(sendCtx, Message{
		To:      mail.Address{Name: msg.ToName, Address: msg.ToEmail},
		Subject: msg.Subject,
		Body:    msg.Body,
	})

	recCtx, recCancel := context.WithTimeout(context.WithoutCancel(ctx), q.opts.SendTimeout)
	defer recCancel()
	wait, err = q.record(recCtx, msg, sendErr)
	if err != nil && wait < errorWait {
		wait = errorWait
	}
	return true, wait, err
}

// record saves the result of sending msg and returns the wait before the
// next email. Logs carry IDs and codes only: never the provider's text.
func (q *Queue) record(ctx context.Context, msg store.QueuedMessage, sendErr error) (time.Duration, error) {
	wait := q.opts.Interval
	var rl *RateLimitError
	switch {
	case sendErr == nil:
		return wait, q.st.MarkSent(ctx, msg.ID)

	case errors.As(sendErr, &rl):
		if msg.Attempts >= maxAttempts {
			slog.Warn("email failed", "batch", msg.BatchID, "message", msg.ID, "code", store.ErrCodeRateLimited)
			return wait, q.st.MarkFailed(ctx, msg.ID, store.ErrCodeRateLimited, sendErr.Error())
		}
		wait = retryAfter(rl.RetryAfter)
		slog.Warn("rate limited", "batch", msg.BatchID, "message", msg.ID, "attempt", msg.Attempts)
		return wait, q.st.ReleaseMessage(ctx, msg.ID)

	case errors.Is(sendErr, ErrAccount):
		slog.Warn("account cannot send, batch stopped", "batch", msg.BatchID, "message", msg.ID)
		if err := q.st.ReleaseMessage(ctx, msg.ID); err != nil {
			return wait, err
		}
		return wait, q.st.StopBatch(ctx, msg.BatchID, store.StopAccount)

	default:
		slog.Warn("email failed", "batch", msg.BatchID, "message", msg.ID, "code", store.ErrCodeSend)
		return wait, q.st.MarkFailed(ctx, msg.ID, store.ErrCodeSend, sendErr.Error())
	}
}

// retryAfter bounds the wait asked by a provider.
func retryAfter(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return defaultRetryAfter
	case d > maxRetryAfter:
		return maxRetryAfter
	default:
		return d
	}
}
