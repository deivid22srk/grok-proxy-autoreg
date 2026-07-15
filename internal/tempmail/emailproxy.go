// EmailProxyProvider implements Provider by calling an external emailproxy
// instance (see cmd/emailproxy). The proxy exposes a unified REST API on
// top of tmaily.com (REST) and invertexto.com (SSE) — both of which
// produce email domains that are NOT in x.ai's typical blocklist, unlike
// mail.tm's web-library.net.
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
        EmailProxyDefaultURL    = "http://127.0.0.1:8788"
        EmailProxyDefaultTimeout = 30 * time.Second
)

// EmailProxyProvider talks to an emailproxy instance.
type EmailProxyProvider struct {
        BaseURL string
        HTTP    *http.Client
        // Backend is "tmaily" (default) or "invertexto".
        Backend string
}

// NewEmailProxyProvider returns an EmailProxyProvider pointing at the
// given proxy URL. The backend defaults to "tmaily".
func NewEmailProxyProvider(baseURL string) *EmailProxyProvider {
        if baseURL == "" {
                baseURL = EmailProxyDefaultURL
        }
        return &EmailProxyProvider{
                BaseURL: strings.TrimRight(baseURL, "/"),
                HTTP:    &http.Client{Timeout: EmailProxyDefaultTimeout},
                Backend: "tmaily",
        }
}

// NewEmailProxyProviderWithBackend lets the caller pick the backend.
func NewEmailProxyProviderWithBackend(baseURL, backend string) *EmailProxyProvider {
        p := NewEmailProxyProvider(baseURL)
        if backend == "invertexto" {
                p.Backend = "invertexto"
        }
        return p
}

func (p *EmailProxyProvider) Name() string {
        return "emailproxy:" + p.Backend
}

// emailProxyInbox is the response from POST /inboxes on the proxy.
type emailProxyInbox struct {
        Address   string `json:"address"`
        Sid       string `json:"sid"`
        Token     string `json:"token"`
        Backend   string `json:"backend"`
        ExpiresIn int    `json:"expires_in"`
}

// CreateInbox asks the proxy to provision a new inbox.
func (p *EmailProxyProvider) CreateInbox(ctx context.Context) (*Inbox, error) {
        backend := p.Backend
        if backend == "" {
                backend = "tmaily"
        }
        body, _ := json.Marshal(map[string]string{"backend": backend})
        req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/inboxes", bytes.NewReader(body))
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
        b, _ := io.ReadAll(resp.Body)
        if resp.StatusCode != 200 {
                return nil, fmt.Errorf("emailproxy /inboxes HTTP %d: %s", resp.StatusCode, string(b))
        }
        var out emailProxyInbox
        if err := json.Unmarshal(b, &out); err != nil {
                return nil, fmt.Errorf("decode emailproxy response: %w (body: %s)", err, string(b))
        }
        if out.Address == "" {
                return nil, fmt.Errorf("emailproxy: empty address (body: %s)", string(b))
        }
        cred := out.Sid
        if cred == "" {
                cred = out.Token
        }
        proxyID := "tmaily:" + cred
        if out.Backend == "invertexto" {
                proxyID = "inv:" + cred
        }
        return &Inbox{
                Address:   out.Address,
                Token:     proxyID + "|" + out.Address,
                Provider:  "emailproxy:" + out.Backend,
                AccountID: cred,
        }, nil
}

// WaitForMessage polls the proxy until a message from fromFilter arrives.
func (p *EmailProxyProvider) WaitForMessage(ctx context.Context, proxyID, fromFilter string, timeout time.Duration) (*Message, error) {
        deadline := time.Now().Add(timeout)
        if fromFilter == "" {
                fromFilter = "x.ai"
        }
        fromFilter = strings.ToLower(fromFilter)
        // The proxy path is /inboxes/{proxyID}/messages?address=...
        // proxyID already carries the backend prefix.
        interval := 3 * time.Second
        firstPoll := true
        for {
                if time.Now().After(deadline) {
                        return nil, fmt.Errorf("timeout aguardando email de %s", fromFilter)
                }
                select {
                case <-ctx.Done():
                        return nil, ctx.Err()
                case <-time.After(interval):
                }
                // We need the address too. The caller passes it via... hmm.
                // Actually, we have to know the address. Let's change the API:
                // the proxyID encodes everything we need (sid+backend), but the
                // proxy still requires ?address= on the GET. So we need to
                // pass the address through the Inbox.
                // Workaround: store the address in the proxyID via a separator.
                // Format: "tmaily:<sid>|<address>" or "inv:<token>|<address>"
                _ = firstPoll
                firstPoll = false
                // Parse proxyID for the address.
                parts := strings.SplitN(proxyID, "|", 2)
                if len(parts) < 2 {
                        return nil, fmt.Errorf("proxyID mal formatado (esperado '<backend>:<cred>|<address>'): %s", proxyID)
                }
                cred := parts[0]
                address := parts[1]
                url := fmt.Sprintf("%s/inboxes/%s/messages?address=%s", p.BaseURL, cred, address)
                req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
                if err != nil {
                        continue
                }
                req.Header.Set("Accept", "application/json")
                resp, err := p.HTTP.Do(req)
                if err != nil {
                        continue
                }
                var out struct {
                        Messages []struct {
                                ID      string `json:"id"`
                                From    string `json:"from"`
                                Subject string `json:"subject"`
                                Text    string `json:"text"`
                                HTML    string `json:"html"`
                                Date    string `json:"date"`
                        } `json:"messages"`
                }
                dec := json.NewDecoder(resp.Body)
                _ = dec.Decode(&out)
                resp.Body.Close()
                for _, m := range out.Messages {
                        if strings.Contains(strings.ToLower(m.From), fromFilter) {
                                receivedAt := time.Now()
                                if m.Date != "" {
                                        if t, err := time.Parse(time.RFC3339, m.Date); err == nil {
                                                receivedAt = t
                                        }
                                }
                                return &Message{
                                        From:       m.From,
                                        Subject:    m.Subject,
                                        Text:       m.Text,
                                        HTML:       m.HTML,
                                        ReceivedAt: receivedAt,
                                }, nil
                        }
                }
        }
}

// DeleteInbox is a no-op for now — the proxy's DELETE endpoint is
// best-effort.
func (p *EmailProxyProvider) DeleteInbox(ctx context.Context, inbox *Inbox) error {
        return nil
}
