package digest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SenderNone is the name recorded when no transport is configured.
const SenderNone = "none"

// SenderLog is the development transport that writes digests to disk.
const SenderLog = "log"

// Message is a rendered digest, ready for whatever transport sends it.
//
// Text and HTML both, because a text/plain alternative is not optional for mail
// that must render in every client — and because the text part is what makes the
// development sender readable at a glance.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// UserID travels with the message so a sender can log which user it was for
	// without parsing the body. Never the email address in logs: hard rule 13.
	UserID string
}

// Sender delivers a digest.
//
// The interface exists because the provider is an open decision
// (docs/OPEN-DECISIONS.md §3) and the rest of step 18 does not depend on it.
// Everything up to the last hop — selection, caps, quiet hours, the empty case,
// idempotency, the decision record — is exercised by the development sender.
type Sender interface {
	// Name is recorded on every digest_send row, so history stays readable
	// across a provider change.
	Name() string
	Send(ctx context.Context, m Message) error
}

// ErrNoTransport is returned by the null sender.
var ErrNoTransport = errors.New(
	"no email transport is configured; set DIGEST_SENDER=log to render digests " +
		"to disk, or configure a provider once the sending-domain decision is settled")

// NullSender refuses to send.
//
// The default, and deliberately not a silent no-op: a digest run that reports
// success while delivering nothing is the exact failure hard rule 26 is about.
// It fails loudly so the outcome is recorded as 'failed' with a reason.
type NullSender struct{}

func (NullSender) Name() string { return SenderNone }

func (NullSender) Send(context.Context, Message) error { return ErrNoTransport }

// LogSender writes each digest to a file and delivers nothing.
//
// This is how step 18 is verifiable without an email provider: the digest that
// would have been sent is on disk, in full, and can be read and diffed. It is
// the same pattern the extraction cache uses with a fake model provider — a real
// interface with a real implementation behind it, so only the last hop is
// unproven.
type LogSender struct {
	Dir   string
	Clock Clock
}

func (LogSender) Name() string { return SenderLog }

func (l LogSender) Send(_ context.Context, m Message) error {
	if err := os.MkdirAll(l.Dir, 0o750); err != nil {
		return fmt.Errorf("digest log dir: %w", err)
	}
	now := time.Now().UTC()
	if l.Clock != nil {
		now = l.Clock.Now()
	}
	// Named by user and timestamp, not by subject: a subject line can contain
	// anything, including a path separator.
	name := fmt.Sprintf("%s-%s.txt", now.Format("20060102T150405"), m.UserID)
	path := filepath.Join(l.Dir, name)

	var b strings.Builder
	// A header block rather than raw body, so the file records what the transport
	// would have been handed. The address is included because this file IS the
	// delivery in development; it is never logged.
	fmt.Fprintf(&b, "To: %s\nSubject: %s\nDate: %s\n\n", m.To, m.Subject,
		now.Format(time.RFC1123Z))
	b.WriteString(m.Text)

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("writing digest: %w", err)
	}
	return nil
}

// NewSender resolves the configured transport.
func NewSender(kind, dir string, clock Clock) (Sender, error) {
	switch kind {
	case "", SenderNone:
		return NullSender{}, nil
	case SenderLog:
		if dir == "" {
			return nil, errors.New("DIGEST_LOG_DIR is required when DIGEST_SENDER=log")
		}
		return LogSender{Dir: dir, Clock: clock}, nil
	default:
		// Named explicitly rather than falling back, so a typo in a deploy config
		// cannot silently disable the retention channel.
		return nil, fmt.Errorf("unknown DIGEST_SENDER %q (%s | %s)",
			kind, SenderNone, SenderLog)
	}
}
