package digest

import (
	"github.com/Xubair001/devsignal/internal/mail"
)

// The transport lives in internal/mail, shared with transactional email.
//
// They share a transport and must never share a consent gate. A user who
// withdraws digest consent still needs to verify an address and reset a
// password, so the consent check stays here — in the package that knows what
// consent means — and not in the transport.
type (
	// Sender delivers a digest. See mail.Sender.
	Sender = mail.Sender
	// Message is a rendered digest. See mail.Message.
	Message = mail.Message
)

// Re-exported so digest callers do not need to know where the transport lives.
var (
	NewSender      = mail.NewSender
	ErrNoTransport = mail.ErrNoTransport
)

// Transport names.
const (
	SenderNone = mail.SenderNone
	SenderLog  = mail.SenderLog
)

// NullSender refuses to send. See mail.NullSender.
type NullSender = mail.NullSender

// LogSender writes to disk and delivers nothing. See mail.LogSender.
type LogSender = mail.LogSender
