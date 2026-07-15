// Helpers for picking a provider and extracting verification links from
// received emails.
package tempmail

import (
        "context"
        "fmt"
        "regexp"
        "strings"
)

// SelectProvider returns a provider by name. Recognized names:
//   - "mail.tm" (default) — mail.tm REST API
//   - "tempmail.lol" — tempmail.lol REST API
//   - "emailproxy" or "emailproxy:tmaily" — proxy → tmaily.com (no Playwright)
//   - "emailproxy:invertexto" — proxy → invertexto.com / uorak.com
//
// The emailproxy backends require a separate `emailproxy` process
// running (see cmd/emailproxy). They produce email domains that are
// NOT in x.ai's typical blocklist (hqpdf.com, uorak.com, etc.), unlike
// mail.tm's web-library.net.
func SelectProvider(name string) (Provider, error) {
        switch strings.ToLower(strings.TrimSpace(name)) {
        case "", "mail.tm", "mailtm", "mail_tm":
                return NewMailTmProvider(), nil
        case "tempmail.lol", "tempmail", "tempmail_lol":
                return NewTempmailLolProvider(), nil
        case "emailproxy", "emailproxy:tmaily", "emailproxy-tmaily":
                return NewEmailProxyProviderWithBackend("", "tmaily"), nil
        case "emailproxy:invertexto", "emailproxy-invertexto":
                return NewEmailProxyProviderWithBackend("", "invertexto"), nil
        default:
                return nil, fmt.Errorf("provedor desconhecido: %q (use mail.tm, tempmail.lol, emailproxy, emailproxy:tmaily ou emailproxy:invertexto)", name)
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

// verifyCodeRegex matches the 6-character code in the format "XXX-XXX"
// that x.ai puts in the subject line of verification emails. Example
// subjects that match:
//
//      "X6B-09B xAI confirmation code"
//      "Your xAI verification code is X6B-09B"
//      "F2K-7P1"
var verifyCodeRegex = regexp.MustCompile(`\b([A-Z0-9]{3})-([A-Z0-9]{3})\b`)

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

// ExtractVerificationCode extracts the 6-character verification code
// (format "XXX-XXX", e.g. "X6B-09B") from the email. It checks the
// subject first (where x.ai puts it most reliably), then falls back
// to the body. Returns "" if no code is found.
//
// This is the correct way to verify x.ai signups — the email contains
// a code that the user must type into the signup form, NOT a clickable
// link. The subject is preferred because it's where x.ai places it:
//
//      "X6B-09B xAI confirmation code"
func ExtractVerificationCode(msg *Message) string {
        if msg == nil {
                return ""
        }
        // Check subject first
        if m := verifyCodeRegex.FindString(msg.Subject); m != "" {
                return m
        }
        // Check body — case insensitive, so the regex is run on uppercased text
        for _, body := range []string{msg.Text, msg.HTML} {
                if body == "" {
                        continue
                }
                upper := strings.ToUpper(body)
                if m := verifyCodeRegex.FindString(upper); m != "" {
                        return m
                }
        }
        return ""
}
