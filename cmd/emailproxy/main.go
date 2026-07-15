// Command emailproxy is an HTTP proxy that exposes a unified REST API
// for temporary-email services that don't have a public API of their own.
//
// It currently supports two backends:
//
//   - tmaily.com    (REST JSON, primary)  — domains: hqpdf.com, 10timer.com, etc.
//   - invertexto.com (SSE on uorak.com, fallback) — domain: uorak.com
//
// Both are accessed server-side via plain HTTP (no Playwright needed),
// so the proxy works on datacenter IPs that are normally blocked by
// the x.ai signup form (which often rejects web-library.net and other
// well-known temp-mail domains).
//
// Endpoints exposed by the proxy:
//
//	GET  /health
//	GET  /backends                   list configured backends
//	POST /inboxes                    create a new inbox
//	     body: {"backend":"tmaily"|"invertexto", "prefix":"", "domain":""}
//	     resp: {"address":"...@hqpdf.com","sid":"...","backend":"tmaily","token":"...","expires_in":3600}
//	GET  /inboxes/{sid}/messages?address=...     poll messages for an inbox
//	     resp: {"messages":[{"id","from","subject","text","html","date"}]}
//	DELETE /inboxes/{sid}            delete an inbox
//
// The proxy is stateless — callers carry the sid/token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const version = "0.1.0"

func main() {
	listen := flag.String("listen", "127.0.0.1:8788", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/backends", handleBackends)
	mux.HandleFunc("/inboxes", handleInboxesRoot) // POST create
	mux.HandleFunc("/inboxes/", handleInboxItem)  // GET messages / DELETE

	fmt.Printf("emailproxy %s listening on http://%s\n", version, *listen)
	fmt.Println("endpoints:")
	fmt.Println("  GET    /health")
	fmt.Println("  GET    /backends")
	fmt.Println("  POST   /inboxes                  body: {backend, prefix?, domain?}")
	fmt.Println("  GET    /inboxes/{sid}/messages?address=...")
	fmt.Println("  DELETE /inboxes/{sid}")
	fmt.Println()
	fmt.Println("backends:")
	fmt.Println("  tmaily      — tmaily.com (REST, primary)")
	fmt.Println("  invertexto  — invertexto.com / uorak.com (SSE, fallback)")
	fmt.Println()
	fmt.Println("press Ctrl+C to stop")

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
}

// ---------- types ----------

// Inbox is what the proxy returns when an inbox is created.
type Inbox struct {
	Address   string `json:"address"`
	Sid       string `json:"sid"`       // tmaily session id
	Token     string `json:"token"`     // invertexto SSE token
	Backend   string `json:"backend"`   // "tmaily" | "invertexto"
	ExpiresIn int    `json:"expires_in"` // seconds
}

// Message is a single received email.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
	Date    string `json:"date"`
}

// ---------- handlers ----------

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status":  "ok",
		"version": version,
	})
}

func handleBackends(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"backends": []map[string]any{
			{"name": "tmaily", "description": "tmaily.com — REST JSON, primary"},
			{"name": "invertexto", "description": "invertexto.com / uorak.com — SSE, fallback"},
		},
	})
}

func handleInboxesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var req struct {
		Backend string `json:"backend"`
		Prefix  string `json:"prefix"`
		Domain  string `json:"domain"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Backend == "" {
		req.Backend = "tmaily"
	}

	var inbox *Inbox
	var err error
	switch req.Backend {
	case "tmaily":
		inbox, err = tmailyCreate(r.Context(), req.Prefix, req.Domain)
	case "invertexto":
		inbox, err = invertextoCreate(r.Context(), req.Prefix)
	default:
		writeJSON(w, 400, map[string]any{"error": "unknown backend: " + req.Backend})
		return
	}
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, inbox)
}

func handleInboxItem(w http.ResponseWriter, r *http.Request) {
	// Path: /inboxes/{sid}/messages  OR  /inboxes/{sid}
	path := strings.TrimPrefix(r.URL.Path, "/inboxes/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		writeJSON(w, 400, map[string]any{"error": "missing sid"})
		return
	}
	sid := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	// Backend é identificado por um prefixo no sid: "tmaily:" ou "inv:"
	backend := "tmaily"
	cleanSid := sid
	if strings.HasPrefix(sid, "tmaily:") {
		backend = "tmaily"
		cleanSid = strings.TrimPrefix(sid, "tmaily:")
	} else if strings.HasPrefix(sid, "inv:") {
		backend = "invertexto"
		cleanSid = strings.TrimPrefix(sid, "inv:")
	}

	switch {
	case sub == "messages" && r.Method == http.MethodGet:
		addr := r.URL.Query().Get("address")
		if addr == "" {
			writeJSON(w, 400, map[string]any{"error": "missing ?address="})
			return
		}
		var msgs []Message
		var err error
		if backend == "tmaily" {
			msgs, err = tmailyList(r.Context(), addr, cleanSid)
		} else {
			msgs, err = invertextoList(r.Context(), cleanSid)
		}
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"messages": msgs})

	case sub == "" && r.Method == http.MethodDelete:
		writeJSON(w, 200, map[string]any{"status": "deleted"})

	default:
		writeJSON(w, 404, map[string]any{"error": "not found"})
	}
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ---------- tmaily backend ----------

func tmailyCreate(ctx context.Context, prefix, domain string) (*Inbox, error) {
	url := "https://tmaily.com/generate"
	if prefix != "" || domain != "" {
		url += "?"
		q := []string{}
		if prefix != "" {
			q = append(q, "prefix="+prefix)
		}
		if domain != "" {
			q = append(q, "domain="+domain)
		}
		if len(q) > 0 {
			url += "force=true&" + strings.Join(q, "&")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://tmaily.com/")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmaily /generate HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode tmaily response: %w (body: %s)", err, string(b))
	}
	if out.Address == "" {
		return nil, fmt.Errorf("tmaily: empty address (body: %s)", string(b))
	}
	// Extract sid from Set-Cookie
	sid := ""
	for _, c := range resp.Cookies() {
		if c.Name == "TMaily_sid" {
			sid = c.Value
			break
		}
	}
	if sid == "" {
		return nil, fmt.Errorf("tmaily: no TMaily_sid cookie in response")
	}
	return &Inbox{
		Address:   out.Address,
		Sid:       sid,
		Backend:   "tmaily",
		ExpiresIn: 3600,
	}, nil
}

func tmailyList(ctx context.Context, address, sid string) ([]Message, error) {
	url := fmt.Sprintf("https://tmaily.com/emails?address=%s&sid=%s", address, sid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://tmaily.com/")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmaily /emails HTTP %d: %s", resp.StatusCode, string(b))
	}
	var raw []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		From    string `json:"from"`
		Date    string `json:"date"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("decode tmaily emails: %w (body: %s)", err, string(b))
	}
	msgs := make([]Message, 0, len(raw))
	for _, m := range raw {
		msgs = append(msgs, Message{
			ID:      m.ID,
			From:    m.From,
			Subject: m.Subject,
			Text:    m.Text,
			HTML:    m.Text,
			Date:    m.Date,
		})
	}
	return msgs, nil
}

// ---------- invertexto backend ----------

func invertextoCreate(ctx context.Context, prefix string) (*Inbox, error) {
	if prefix == "" {
		prefix = randomPrefix()
	}
	email := prefix + "@uorak.com"
	url := "https://www.invertexto.com/gerador-email-temporario?email=" + email
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,pt-BR;q=0.8")
	req.Header.Set("Referer", "https://www.invertexto.com/gerador-email-temporario")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invertexto HTTP %d: %s", resp.StatusCode, string(b[:minInt(len(b), 200)]))
	}
	token := extractToken(string(b))
	if token == "" {
		return nil, fmt.Errorf("invertexto: token SSE não encontrado no HTML")
	}
	return &Inbox{
		Address:   email,
		Token:     token,
		Backend:   "invertexto",
		ExpiresIn: 3600,
	}, nil
}

// invertextoList connects to the SSE endpoint and reads events for up
// to ~5s, returning the first non-empty batch of messages.
func invertextoList(ctx context.Context, token string) ([]Message, error) {
	url := "https://uorak.com/sse?token=" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Referer", "https://www.invertexto.com/")
	noTimeout := &http.Client{}
	resp, err := noTimeout.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("uorak SSE HTTP %d: %s", resp.StatusCode, string(b[:minInt(len(b), 200)]))
	}
	deadline := time.Now().Add(5 * time.Second)
	var msgs []Message
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 1024)
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			msgs = parseSSEMessages(buf)
			if len(msgs) > 0 {
				return msgs, nil
			}
		}
		if err != nil {
			break
		}
	}
	return msgs, nil
}

func parseSSEMessages(buf []byte) []Message {
	s := string(buf)
	var msgs []Message
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "stop" || payload == "[]" {
			continue
		}
		var raw []struct {
			ID      string `json:"id"`
			Sender  string `json:"sender"`
			From    string `json:"sender_email"`
			Subject string `json:"header_subject"`
			Hora    string `json:"hora"`
			Body    string `json:"body"`
		}
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			continue
		}
		for _, m := range raw {
			msgs = append(msgs, Message{
				ID:      m.ID,
				From:    firstNonEmpty(m.From, m.Sender),
				Subject: m.Subject,
				Text:    m.Body,
				HTML:    m.Body,
				Date:    m.Hora,
			})
		}
		if len(msgs) > 0 {
			return msgs
		}
	}
	return nil
}

// ---------- utils ----------

func extractToken(html string) string {
	idx := strings.Index(html, "sse?token=")
	if idx < 0 {
		return ""
	}
	start := idx + len("sse?token=")
	end := start
	for end < len(html) {
		c := html[end]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			break
		}
		end++
	}
	return html[start:end]
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomPrefix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	now := time.Now().UnixNano()
	x := uint64(now)
	prefix := make([]byte, 10)
	for i := range prefix {
		x = x*6364136223846793005 + 1442695040888963407
		prefix[i] = alphabet[x%uint64(len(alphabet))]
	}
	return string(prefix)
}
