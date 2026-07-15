// TempmailLolProvider implements Provider against the public tempmail.lol API.
// Used as a fallback when mail.tm is unavailable or rejects a domain.
package tempmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	TempmailLolBaseURL = "https://api.tempmail.lol"
	TempmailLolTimeout = 30 * time.Second
)

// TempmailLolProvider is the tempmail.lol client.
type TempmailLolProvider struct {
	BaseURL string
	HTTP    *http.Client
}

// NewTempmailLolProvider returns a TempmailLolProvider with sane defaults.
func NewTempmailLolProvider() *TempmailLolProvider {
	return &TempmailLolProvider{
		BaseURL: TempmailLolBaseURL,
		HTTP:    &http.Client{Timeout: TempmailLolTimeout},
	}
}

// tempmailLolCreateResponse is the body returned by POST /v2/inbox/create.
type tempmailLolCreateResponse struct {
	Address string `json:"address"`
	Token   string `json:"token"`
}

// tempmailLolInboxResponse is the body returned by GET /v2/inbox.
type tempmailLolInboxResponse struct {
	Emails  []tempmailLolEmail `json:"emails"`
	Expired bool               `json:"expired"`
}

// tempmailLolEmail is a single email in the inbox.
type tempmailLolEmail struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	HTML    string `json:"html"`
	Date    int64  `json:"date"`
}

// Name returns the provider name.
func (p *TempmailLolProvider) Name() string { return "tempmail.lol" }

// CreateInbox provisions a new random inbox on tempmail.lol.
func (p *TempmailLolProvider) CreateInbox(ctx context.Context) (*Inbox, error) {
	if p.BaseURL == "" {
		p.BaseURL = TempmailLolBaseURL
	}
	if p.HTTP == nil {
		p.HTTP = &http.Client{Timeout: TempmailLolTimeout}
	}
	body, _ := json.Marshal(map[string]string{})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v2/inbox/create", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tempmail.lol /v2/inbox/create HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out tempmailLolCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode tempmail.lol response: %w", err)
	}
	if out.Address == "" || out.Token == "" {
		return nil, fmt.Errorf("tempmail.lol: resposta vazia")
	}
	return &Inbox{
		Address:  out.Address,
		Token:    out.Token,
		Provider: "tempmail.lol",
	}, nil
}

// WaitForMessage polls the inbox until a message from fromFilter arrives.
func (p *TempmailLolProvider) WaitForMessage(ctx context.Context, token, fromFilter string, timeout time.Duration) (*Message, error) {
	if p.BaseURL == "" {
		p.BaseURL = TempmailLolBaseURL
	}
	if p.HTTP == nil {
		p.HTTP = &http.Client{Timeout: TempmailLolTimeout}
	}
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/v2/inbox?token="+token, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		resp, err := p.HTTP.Do(req)
		if err != nil {
			continue
		}
		var out tempmailLolInboxResponse
		dec := json.NewDecoder(resp.Body)
		_ = dec.Decode(&out)
		resp.Body.Close()
		if out.Expired {
			return nil, fmt.Errorf("inbox expirado")
		}
		for _, e := range out.Emails {
			if strings.Contains(strings.ToLower(e.From), fromFilter) {
				receivedAt := time.Now()
				if e.Date > 0 {
					receivedAt = time.Unix(e.Date, 0)
				}
				return &Message{
					From:        e.From,
					To:          e.To,
					Subject:     e.Subject,
					Text:        e.Body,
					HTML:        e.HTML,
					ReceivedAt:  receivedAt,
					ProviderMsg: e,
				}, nil
			}
		}
	}
}

// DeleteInbox is a no-op on tempmail.lol — the inbox auto-expires in 1h.
func (p *TempmailLolProvider) DeleteInbox(ctx context.Context, inbox *Inbox) error {
	return nil
}
