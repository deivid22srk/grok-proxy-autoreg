// Helpers for picking a provider and extracting verification links from
// received emails.
package tempmail

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SelectProvider returns a provider by name. Recognized names: "mail.tm",
// "tempmail.lol". An empty name returns the default ("mail.tm").
func SelectProvider(name string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "mail.tm", "mailtm", "mail_tm":
		return NewMailTmProvider(), nil
	case "tempmail.lol", "tempmail", "tempmail_lol":
		return NewTempmailLolProvider(), nil
	default:
		return nil, fmt.Errorf("provedor desconhecido: %q (use mail.tm ou tempmail.lol)", name)
	}
}

// AllProviders returns a slice of providers in the recommended order.
func AllProviders() []Provider {
	return []Provider{
		NewMailTmProvider(),
		NewTempmailLolProvider(),
	}
}

// CreateInboxWithFallback tries each provider in order until one succeeds.
// Returns the inbox and the provider that produced it.
func CreateInboxWithFallback(ctx context.Context, providers []Provider) (*Inbox, Provider, error) {
	if len(providers) == 0 {
		providers = AllProviders()
	}
	var lastErr error
	for _, p := range providers {
		inbox, err := p.CreateInbox(ctx)
		if err == nil {
			return inbox, p, nil
		}
		lastErr = fmt.Errorf("%s: %w", p.Name(), err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum provedor disponível")
	}
	return nil, nil, lastErr
}

// verifyLinkRegex matches http(s) URLs that look like an email-verification
// link from x.ai/Grok. We accept any URL in the body but prefer ones that
// contain common verification keywords ("verify", "confirm", "activate",
// "x.ai", "grok.com", "auth.x.ai").
var verifyLinkRegex = regexp.MustCompile(`https?://[^\s"'<>\)\]]+`)

// verifyKeywordRegex further filters URLs that look like verification links.
var verifyKeywordRegex = regexp.MustCompile(`(?i)(verify|verifica|confirm|confirma|activate|ativa|email|account|grok\.com|x\.ai|auth\.x\.ai)`)

// ExtractVerificationLink scans the message body (HTML first, then plain text)
// for a URL that looks like a verification link from x.ai/Grok.
//
// Returns the first match that contains a verification keyword; if none match
// the keyword filter, returns the first URL found (best-effort).
func ExtractVerificationLink(msg *Message) string {
	if msg == nil {
		return ""
	}
	candidates := []string{msg.HTML, msg.Text}
	var firstURL string
	for _, body := range candidates {
		if body == "" {
			continue
		}
		matches := verifyLinkRegex.FindAllString(body, -1)
		for _, m := range matches {
			// strip trailing punctuation commonly present in email bodies
			m = strings.TrimRight(m, ".,;:`'\"")
			if firstURL == "" {
				firstURL = m
			}
			if verifyKeywordRegex.MatchString(m) {
				return m
			}
		}
	}
	return firstURL
}
