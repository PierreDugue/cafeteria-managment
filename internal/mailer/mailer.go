// Package mailer sends the emails of a batch one at a time in the
// background (SPEC §4.4, §5.3). Providers implement Sender; the queue, the
// database and the UI do not depend on which one is used.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"cafeteria-management/internal/store"
)

// Provider names, recorded on batches (batches.provider).
const (
	ProviderTest  = "test"  // test mode: nothing is sent
	ProviderGraph = "graph" // Microsoft 365 through Microsoft Graph
)

// Provider is a sender with the wait to keep after each of its emails
// (rate limits, SPEC §5.1).
type Provider struct {
	Sender   Sender
	Interval time.Duration
}

// Message is one email for a Sender. To is structured: a provider must use
// its name and address fields (Graph emailAddress, go-mail AddrFullName),
// never a header built by string concatenation. The sender address is the
// account's own, so there is no From.
type Message struct {
	To      mail.Address
	Subject string // one line, at most 255 runes (checked when the batch is created)
	Body    string // plain text, "\n" line endings
}

// Sender sends one email. It returns nil once the provider accepted it.
// Besides plain errors (the email failed), it can return a *RateLimitError
// or an error wrapping ErrAccount; in both cases the email was not sent.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// RateLimitError means the provider asks to wait before the next email
// (HTTP 429 / Retry-After). The email was not sent.
type RateLimitError struct {
	RetryAfter time.Duration // 0 if the provider gave no delay
	Err        error
}

// Error implements error.
func (e *RateLimitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
	}
	return fmt.Sprintf("rate limited, retry after %s: %v", e.RetryAfter, e.Err)
}

// Unwrap returns the provider's error.
func (e *RateLimitError) Unwrap() error { return e.Err }

// ErrAccount means the account cannot send at all (not signed in, access
// revoked). The email was not sent. Providers wrap it with %w.
var ErrAccount = errors.New("email account cannot send")

// ErrUncertain means the provider may have processed the email (the
// connection was lost or timed out after the request was sent). The email
// must not be retried automatically: that is how a parent gets it twice.
// Providers wrap it with %w.
var ErrUncertain = errors.New("email may have been sent")

// TestSender accepts every email and sends nothing: it keeps no file and
// does not log the content. The history is the only trace (test mode).
type TestSender struct{}

// Send implements Sender.
func (TestSender) Send(context.Context, Message) error { return nil }

// ErrorCode classifies a Sender error with the code recorded on a failed
// message (store.ErrCode*): rate_limited, account, unknown, or send.
func ErrorCode(err error) string {
	var rl *RateLimitError
	switch {
	case errors.As(err, &rl):
		return store.ErrCodeRateLimited
	case errors.Is(err, ErrAccount):
		return store.ErrCodeAccount
	case errors.Is(err, ErrUncertain):
		return store.ErrCodeUnknown
	default:
		return store.ErrCodeSend
	}
}
