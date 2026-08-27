package mail

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

// Clock is injected. Hard rule 14: the timestamp on a rendered message is
// domain-visible, and time.Now() inside it is untestable.
type Clock interface{ Now() time.Time }

// Message is a rendered email, ready for whatever transport sends it.
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
	// Kind labels the message for the development sender's filename, so a
	// verification email and a digest are distinguishable on disk without
	// opening them. Never part of the delivered message.
	Kind string
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Sender delivers an email.
//
// Shared by transactional mail (email verification, password reset) and by the
// daily digest. They share a TRANSPORT and must never share a consent gate: a
// user who withdraws digest consent still needs to be able to verify an address
// or reset a password, so the consent check lives in internal/digest and
// deliberately not here.
//
// The interface exists because the provider is an open decision
// (docs/OPEN-DECISIONS.md §3) and nothing else depends on which one wins.
// Everything up to the last hop is exercised by the development sender.
type Sender interface {
	// Name is recorded on every digest_send row, so history stays readable
	// across a provider change.
	Name() string
	Send(ctx context.Context, m Message) error
}

// ErrNoTransport is returned by the null sender.
var ErrNoTransport = errors.New(
	"no email transport is configured; set MAIL_SENDER=log to render mail to " +
		"disk, or configure a provider once the sending-domain decision is settled")

// NullSender refuses to send.
//
// The default, and deliberately not a silent no-op: a run that reports success
// while delivering nothing is the exact failure hard rule 26 is about. It fails
// loudly so the outcome is recorded as 'failed' with a reason.
type NullSender struct{}

func (NullSender) Name() string { return SenderNone }

func (NullSender) Send(context.Context, Message) error { return ErrNoTransport }

// LogSender writes each message to a file and delivers nothing.
//
// This is how mail is verifiable without a provider: what would have been sent
// is on disk, in full, and can be read and diffed. It is the same pattern
// extraction uses with a fake model provider — a real interface with a real
// implementation behind it, so only the last hop is unproven.
//
// For a verification email that also makes the flow usable end to end: the link
// is in the file, and a developer can follow it.
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
	name := fmt.Sprintf("%s-%s-%s.txt", now.Format("20060102T150405"),
		nonEmpty(m.Kind, "mail"), m.UserID)
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
			return nil, errors.New("MAIL_LOG_DIR is required when MAIL_SENDER=log")
		}
		return LogSender{Dir: dir, Clock: clock}, nil
	default:
		// Named explicitly rather than falling back, so a typo in a deploy config
		// cannot silently disable the retention channel.
		return nil, fmt.Errorf("unknown MAIL_SENDER %q (%s | %s)",
			kind, SenderNone, SenderLog)
	}
}
