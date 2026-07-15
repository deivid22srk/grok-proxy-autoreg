// Provider-agnostic types & interface shared by all temp-mail backends.
package tempmail

import (
	"context"
	"time"
)

// Inbox is the handle returned by CreateInbox. It carries everything the
// caller needs to read incoming mail and clean up afterwards.
type Inbox struct {
	Address   string // full email address (e.g. abc@web-library.net)
	Password  string // password used to create the inbox (mail.tm only)
	Token     string // JWT / opaque token used to authenticate subsequent calls
	Provider  string // "mail.tm" | "tempmail.lol"
	AccountID string // mail.tm account id (used by DELETE /accounts/{id})
}

// Provider abstracts a temp-mail backend.
type Provider interface {
	Name() string
	CreateInbox(ctx context.Context) (*Inbox, error)
	WaitForMessage(ctx context.Context, token, fromFilter string, timeout time.Duration) (*Message, error)
	DeleteInbox(ctx context.Context, inbox *Inbox) error
}

// Message is the unified, provider-agnostic representation of a received
// email. It exposes both the plain-text and HTML bodies so callers can pick
// whichever is easiest to scrape for the verification link.
type Message struct {
	From        string
	To          string
	Subject     string
	Text        string
	HTML        string
	ReceivedAt  time.Time
	ProviderMsg any // underlying provider-specific struct, in case callers need it
}
