// Package tempmail provides temporary-email clients used by the auto-register
// flow to receive the verification email that x.ai/Grok sends during signup.
//
// Two providers are supported:
//
//   - Mail.tm   (primary)   — https://api.mail.tm   — REST + JWT, no API key
//   - TempmailLol (fallback) — https://api.tempmail.lol — token-based, 1h inbox
//
// Both providers implement the Provider interface so callers can swap them
// without changing the consuming code.
package tempmail

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "math/rand"
        "net/http"
        "strings"
        "time"
)

const (
        MailTmBaseURL = "https://api.mail.tm"
        MailTmTimeout = 30 * time.Second
)

// MailTmProvider implements Provider against the public mail.tm API.
type MailTmProvider struct {
        BaseURL string
        HTTP    *http.Client
}

// NewMailTmProvider returns a MailTmProvider with sane defaults.
func NewMailTmProvider() *MailTmProvider {
        return &MailTmProvider{
                BaseURL: MailTmBaseURL,
                HTTP:    &http.Client{Timeout: MailTmTimeout},
        }
}

// MailTmDomain is a single active domain returned by GET /domains.
type MailTmDomain struct {
        ID        string `json:"id"`
        Domain    string `json:"domain"`
        IsActive  bool   `json:"isActive"`
        IsPrivate bool   `json:"isPrivate"`
}

// MailTmAccount is the account created via POST /accounts.
type MailTmAccount struct {
        Context    string `json:"@context"`
        Type       string `json:"@type"`
        ID         string `json:"id"`
        Address    string `json:"address"`
        Quota      int64  `json:"quota"`
        Used       int64  `json:"used"`
        IsDisabled bool   `json:"isDisabled"`
        IsDeleted  bool   `json:"isDeleted"`
        CreatedAt  string `json:"createdAt"`
}

// MailTmToken is the JWT bundle returned by POST /token.
type MailTmToken struct {
        Token string `json:"token"`
        ID    string `json:"id"`
}

// MailTmMessageSummary is one row from GET /messages.
type MailTmMessageSummary struct {
        ID        string          `json:"id"`
        From      MailTmAddress   `json:"from"`
        To        []MailTmAddress `json:"to"`
        Subject   string          `json:"subject"`
        Intro     string          `json:"intro"`
        Seen      bool            `json:"seen"`
        HasAttach bool            `json:"hasAttachments"`
        Size      int64           `json:"size"`
        CreatedAt string          `json:"createdAt"`
}

// MailTmAddress is a {name,address} pair.
type MailTmAddress struct {
        Name    string `json:"name"`
        Address string `json:"address"`
}

// MailTmMessage is the full message (with body) from GET /messages/{id}.
type MailTmMessage struct {
        MailTmMessageSummary
        Text        string   `json:"text"`
        HTML        []string `json:"html"`
        Attachments []any    `json:"attachments"`
}

// GetDomains lists active domains from mail.tm.
func (p *MailTmProvider) GetDomains(ctx context.Context) ([]MailTmDomain, error) {
        if p.BaseURL == "" {
                p.BaseURL = MailTmBaseURL
        }
        if p.HTTP == nil {
                p.HTTP = &http.Client{Timeout: MailTmTimeout}
        }
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/domains", nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("Accept", "application/ld+json")
        resp, err := p.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 400 {
                b, _ := io.ReadAll(resp.Body)
                return nil, fmt.Errorf("mail.tm /domains HTTP %d: %s", resp.StatusCode, string(b))
        }
        var out struct {
                Total   int             `json:"hydra:totalItems"`
                Members []MailTmDomain  `json:"hydra:member"`
                _       struct{}        `json:"-"`
        }
        // decode ignoring unknown fields
        if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
                return nil, fmt.Errorf("decode domains: %w", err)
        }
        // filter to active
        var active []MailTmDomain
        for _, d := range out.Members {
                if d.IsActive {
                        active = append(active, d)
                }
        }
        if len(active) == 0 {
                return nil, fmt.Errorf("mail.tm: nenhum domínio ativo")
        }
        return active, nil
}

// randomLocalPart generates a random local part (6-12 chars, alphanumeric).
func randomLocalPart() string {
        const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
        r := rand.New(rand.NewSource(time.Now().UnixNano()))
        n := 8 + r.Intn(5)
        b := make([]byte, n)
        for i := range b {
                b[i] = alphabet[r.Intn(len(alphabet))]
        }
        return string(b)
}

// randomPassword generates a strong-enough password for mail.tm (>=8 chars).
func randomPassword() string {
        const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
        r := rand.New(rand.NewSource(time.Now().UnixNano()))
        b := make([]byte, 16)
        for i := range b {
                b[i] = alphabet[r.Intn(len(alphabet))]
        }
        return string(b)
}

// CreateInbox provisions a brand-new inbox on mail.tm and returns an
// Inbox handle containing the address, password and JWT token.
func (p *MailTmProvider) CreateInbox(ctx context.Context) (*Inbox, error) {
        if p.BaseURL == "" {
                p.BaseURL = MailTmBaseURL
        }
        if p.HTTP == nil {
                p.HTTP = &http.Client{Timeout: MailTmTimeout}
        }
        domains, err := p.GetDomains(ctx)
        if err != nil {
                return nil, err
        }
        localPart := randomLocalPart()
        addr := fmt.Sprintf("%s@%s", localPart, domains[0].Domain)
        pass := randomPassword()

        // POST /accounts
        body, _ := json.Marshal(map[string]string{
                "address":  addr,
                "password": pass,
        })
        req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/accounts", bytes.NewReader(body))
        if err != nil {
                return nil, err
        }
        req.Header.Set("Content-Type", "application/ld+json")
        req.Header.Set("Accept", "application/ld+json")
        resp, err := p.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        if resp.StatusCode >= 400 {
                b, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                return nil, fmt.Errorf("mail.tm /accounts HTTP %d: %s", resp.StatusCode, string(b))
        }
        var acc MailTmAccount
        _ = json.NewDecoder(resp.Body).Decode(&acc)
        resp.Body.Close()

        // POST /token
        body, _ = json.Marshal(map[string]string{
                "address":  addr,
                "password": pass,
        })
        req, err = http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/token", bytes.NewReader(body))
        if err != nil {
                return nil, err
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Accept", "application/json")
        resp, err = p.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 400 {
                b, _ := io.ReadAll(resp.Body)
                return nil, fmt.Errorf("mail.tm /token HTTP %d: %s", resp.StatusCode, string(b))
        }
        var tok MailTmToken
        if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
                return nil, fmt.Errorf("decode token: %w", err)
        }
        if tok.Token == "" {
                return nil, fmt.Errorf("mail.tm: token vazio")
        }
        return &Inbox{
                Address:   addr,
                Password:  pass,
                Token:     tok.Token,
                Provider:  "mail.tm",
                AccountID: acc.ID,
        }, nil
}

// ListMessages lists message summaries for the inbox identified by its JWT.
func (p *MailTmProvider) ListMessages(ctx context.Context, token string) ([]MailTmMessageSummary, error) {
        if p.BaseURL == "" {
                p.BaseURL = MailTmBaseURL
        }
        if p.HTTP == nil {
                p.HTTP = &http.Client{Timeout: MailTmTimeout}
        }
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/messages", nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("Accept", "application/ld+json")
        req.Header.Set("Authorization", "Bearer "+token)
        resp, err := p.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 400 {
                b, _ := io.ReadAll(resp.Body)
                return nil, fmt.Errorf("mail.tm /messages HTTP %d: %s", resp.StatusCode, string(b))
        }
        var out struct {
                Members []MailTmMessageSummary `json:"hydra:member"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
                return nil, fmt.Errorf("decode messages: %w", err)
        }
        return out.Members, nil
}

// GetMessage fetches the full body of a message.
func (p *MailTmProvider) GetMessage(ctx context.Context, token, id string) (*MailTmMessage, error) {
        if p.BaseURL == "" {
                p.BaseURL = MailTmBaseURL
        }
        if p.HTTP == nil {
                p.HTTP = &http.Client{Timeout: MailTmTimeout}
        }
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/messages/"+id, nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("Accept", "application/ld+json")
        req.Header.Set("Authorization", "Bearer "+token)
        resp, err := p.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 400 {
                b, _ := io.ReadAll(resp.Body)
                return nil, fmt.Errorf("mail.tm /messages/%s HTTP %d: %s", id, resp.StatusCode, string(b))
        }
        var msg MailTmMessage
        if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
                return nil, fmt.Errorf("decode message: %w", err)
        }
        return &msg, nil
}

// deleteAccount removes the account from mail.tm (cleanup).
func (p *MailTmProvider) deleteAccount(ctx context.Context, token, accountID string) error {
        if accountID == "" {
                return nil
        }
        if p.BaseURL == "" {
                p.BaseURL = MailTmBaseURL
        }
        if p.HTTP == nil {
                p.HTTP = &http.Client{Timeout: MailTmTimeout}
        }
        req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.BaseURL+"/accounts/"+accountID, nil)
        if err != nil {
                return err
        }
        req.Header.Set("Authorization", "Bearer "+token)
        resp, err := p.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        return nil
}

// WaitForMessageRaw polls /messages until a message matching fromFilter arrives
// or timeout elapses. fromFilter is matched case-insensitively against the
// sender address (e.g. "x.ai" matches "noreply@x.ai"). Returns the raw
// mail.tm message type with all fields.
func (p *MailTmProvider) WaitForMessageRaw(ctx context.Context, token, fromFilter string, timeout time.Duration) (*MailTmMessage, error) {
        deadline := time.Now().Add(timeout)
        if fromFilter == "" {
                fromFilter = "x.ai"
        }
        fromFilter = strings.ToLower(fromFilter)
        interval := 3 * time.Second
        for {
                if time.Now().After(deadline) {
                        return nil, fmt.Errorf("timeout aguardando email de %s", fromFilter)
                }
                select {
                case <-ctx.Done():
                        return nil, ctx.Err()
                case <-time.After(interval):
                }
                msgs, err := p.ListMessages(ctx, token)
                if err != nil {
                        // transient errors — keep polling
                        continue
                }
                for _, m := range msgs {
                        if strings.Contains(strings.ToLower(m.From.Address), fromFilter) {
                                full, err := p.GetMessage(ctx, token, m.ID)
                                if err == nil {
                                        return full, nil
                                }
                        }
                }
        }
}

// WaitForMessage implements Provider. Polls /messages until a message from
// fromFilter arrives, then returns a unified Message.
func (p *MailTmProvider) WaitForMessage(ctx context.Context, token, fromFilter string, timeout time.Duration) (*Message, error) {
        raw, err := p.WaitForMessageRaw(ctx, token, fromFilter, timeout)
        if err != nil {
                return nil, err
        }
        html := ""
        if len(raw.HTML) > 0 {
                html = raw.HTML[0]
        }
        toAddr := ""
        if len(raw.To) > 0 {
                toAddr = raw.To[0].Address
        }
        receivedAt := time.Now()
        if t, err := time.Parse(time.RFC3339, raw.CreatedAt); err == nil {
                receivedAt = t
        }
        return &Message{
                From:        raw.From.Address,
                To:          toAddr,
                Subject:     raw.Subject,
                Text:        raw.Text,
                HTML:        html,
                ReceivedAt:  receivedAt,
                ProviderMsg: raw,
        }, nil
}

// DeleteInbox implements Provider.
func (p *MailTmProvider) DeleteInbox(ctx context.Context, inbox *Inbox) error {
        if inbox == nil {
                return nil
        }
        return p.DeleteInboxByID(ctx, inbox.Token, inbox.AccountID)
}

// DeleteInboxByID is the lower-level delete used by the Provider adapter.
func (p *MailTmProvider) DeleteInboxByID(ctx context.Context, token, accountID string) error {
        if accountID == "" {
                return nil
        }
        return p.deleteAccount(ctx, token, accountID)
}

// Name returns the provider name.
func (p *MailTmProvider) Name() string { return "mail.tm" }
