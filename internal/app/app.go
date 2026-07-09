// Package app provides the shared core of Grok Proxy Plus without any GUI
// dependency. It is consumed by:
//   - the original desktop build (main.go + app.go via Wails)
//   - the terminal-only build (cmd/grok-proxy-cli)
//
// The package wires together the OAuth client, the multi-account store,
// the upstream proxy client, and the local OpenAI/Anthropic-compatible
// HTTP server. Public methods are safe to call from any goroutine.
package app

import (
        "context"
        "errors"
        "fmt"
        "path/filepath"
        "strings"
        "sync"
        "time"

        "grok-desktop/internal/mcpconfig"
        "grok-desktop/internal/oauth"
        "grok-desktop/internal/pricing"
        "grok-desktop/internal/proxyhttp"
        "grok-desktop/internal/skills"
        "grok-desktop/internal/store"
        "grok-desktop/internal/upstream"
)

// App is the headless core. Construct with Open.
type App struct {
        mu       sync.Mutex
        store    *store.Store
        oauth    *oauth.Client
        upstream *upstream.Client
        proxy    *proxyhttp.Server
        skills   *skills.Store
        mcp      *mcpconfig.Store

        deviceCancel context.CancelFunc
        deviceStart *oauth.DeviceStart
}

// Open initializes the store under the default AppData directory (or root).
func Open(root string) (*App, error) {
        st, err := store.Open(root)
        if err != nil {
                return nil, err
        }
        a := &App{
                store:    st,
                oauth:    oauth.New(),
                upstream: upstream.New(),
                proxy:    proxyhttp.New(st, nil, nil), // ensure set below
        }
        a.proxy = proxyhttp.New(st, a.upstream, a.ensureCreds)
        if sk, err := skills.Open(filepath.Join(st.Root(), "skills")); err == nil {
                a.skills = sk
        }
        if mc, err := mcpconfig.Open(filepath.Join(st.Root(), "mcp_servers.json")); err == nil {
                a.mcp = mc
        }
        return a, nil
}

// Close releases any persistent resources (currently a no-op).
func (a *App) Close() error { return nil }

// DataDir returns the on-disk AppData root.
func (a *App) DataDir() string { return a.store.Root() }

// Settings returns a snapshot of the persisted settings.
func (a *App) Settings() store.Settings { return a.store.Settings() }

// UpdateSettings mutates settings atomically.
func (a *App) UpdateSettings(fn func(*store.Settings)) error {
        return a.store.UpdateSettings(fn)
}

// ---------- Account management ----------

// AccountInfo is a public projection of a stored account (no secrets).
type AccountInfo struct {
        ID        string    `json:"id"`
        Label     string    `json:"label"`
        Email     string    `json:"email"`
        ExpiresAt time.Time `json:"expires_at"`
        Expired   bool      `json:"expired"`
}

// ListAccounts returns all configured accounts.
func (a *App) ListAccounts() []AccountInfo {
        out := make([]AccountInfo, 0)
        for _, acc := range a.store.ListAccounts() {
                out = append(out, AccountInfo{
                        ID:        acc.ID,
                        Label:     acc.Label,
                        Email:     acc.Email,
                        ExpiresAt: acc.ExpiresAt,
                        Expired:   acc.Expired(),
                })
        }
        return out
}

// ActiveAccountID returns the active account id, if any.
func (a *App) ActiveAccountID() string {
        acc, ok := a.store.ActiveAccount()
        if !ok {
                return ""
        }
        return acc.ID
}

// ActiveAccount returns the active account, if any.
func (a *App) ActiveAccount() (store.Account, bool) {
        acc, ok := a.store.ActiveAccount()
        if !ok || acc == nil {
                return store.Account{}, false
        }
        return *acc, true
}

// SetActiveAccount makes the given account id active.
func (a *App) SetActiveAccount(id string) error { return a.store.SetActiveAccount(id) }

// RemoveAccount deletes an account from the store.
func (a *App) RemoveAccount(id string) error { return a.store.RemoveAccount(id) }

// ResolveAccountID accepts an id prefix (first 8+ chars) and returns the full id.
func (a *App) ResolveAccountID(prefix string) (string, error) {
        accs := a.store.ListAccounts()
        if len(accs) == 0 {
                return "", errors.New("no accounts configured")
        }
        // exact match first
        for _, acc := range accs {
                if acc.ID == prefix {
                        return acc.ID, nil
                }
        }
        // prefix match
        var matches []string
        for _, acc := range accs {
                if strings.HasPrefix(acc.ID, prefix) {
                        matches = append(matches, acc.ID)
                }
        }
        if len(matches) == 1 {
                return matches[0], nil
        }
        if len(matches) == 0 {
                return "", fmt.Errorf("no account matching %q", prefix)
        }
        return "", fmt.Errorf("ambiguous prefix %q matches %d accounts", prefix, len(matches))
}

// ---------- Login ----------

// DeviceLogin holds the information needed to display a device-code login.
type DeviceLogin struct {
        DeviceCode      string
        UserCode        string
        VerificationURL string
        Interval        int
        ExpiresIn       int
}

// StartDeviceLogin kicks off the OAuth device-code flow.
func (a *App) StartDeviceLogin(ctx context.Context) (*DeviceLogin, error) {
        a.mu.Lock()
        if a.deviceCancel != nil {
                a.deviceCancel()
        }
        a.mu.Unlock()

        // Use a long-lived context (not tied to the caller's) so that the
        // subsequent WaitDeviceLogin call can still poll the same device code.
        pollCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
        start, err := a.oauth.StartDevice(pollCtx)
        if err != nil {
                cancel()
                return nil, err
        }
        a.mu.Lock()
        a.deviceCancel = cancel
        a.deviceStart = start
        a.mu.Unlock()

        url := start.VerificationURIComplete
        if url == "" {
                url = start.VerificationURI
        }
        return &DeviceLogin{
                DeviceCode:      start.DeviceCode,
                UserCode:        start.UserCode,
                VerificationURL: url,
                Interval:        start.Interval,
                ExpiresIn:       start.ExpiresIn,
        }, nil
}

// WaitDeviceLogin blocks until the user completes the device-code flow.
// It must be called after StartDeviceLogin. The same device code shown to
// the user is polled — a fresh one is NOT issued (otherwise the user would
// authorize one code while we polled another, never completing).
func (a *App) WaitDeviceLogin(ctx context.Context) (store.Account, error) {
        a.mu.Lock()
        cancel := a.deviceCancel
        start := a.deviceStart
        a.mu.Unlock()
        if cancel == nil || start == nil {
                return store.Account{}, errors.New("no device login in progress")
        }
        defer func() {
                a.mu.Lock()
                a.deviceCancel = nil
                a.deviceStart = nil
                a.mu.Unlock()
                cancel()
        }()

        // Tie the polling to the caller's ctx so Ctrl+C in the CLI cancels it,
        // but cap it at the device code lifetime as a safety net.
        pollCtx, pollCancel := context.WithTimeout(ctx, time.Duration(start.ExpiresIn)*time.Second)
        defer pollCancel()

        tok, err := a.oauth.PollDevice(pollCtx, start.DeviceCode, start.Interval)
        if err != nil {
                return store.Account{}, err
        }
        acc := oauth.AccountFromToken(tok, a.oauth.ClientID, a.oauth.Issuer)
        email, uid := a.oauth.UserInfo(ctx, tok.AccessToken, a.oauth.Issuer)
        if email != "" {
                acc.Email = email
        }
        if uid != "" {
                acc.UserID = uid
                acc.ID = uid
        }
        if prev, ok := a.store.GetAccount(acc.ID); ok && prev != nil {
                if prev.Label != "" && prev.Label != prev.Email && prev.Label != "Grok account" {
                        acc.Label = prev.Label
                }
                acc.CreatedAt = prev.CreatedAt
        }
        if acc.Label == "" || acc.Label == "Grok account" {
                if acc.Email != "" {
                        acc.Label = acc.Email
                } else if len(acc.ID) >= 8 {
                        acc.Label = "Conta " + acc.ID[:8]
                } else {
                        acc.Label = "Conta"
                }
        }
        if err := a.store.UpsertAccount(acc); err != nil {
                return store.Account{}, err
        }
        _ = a.store.SetActiveAccount(acc.ID)
        return acc, nil
}

// ---------- Proxy ----------

// StartProxy starts the local HTTP proxy on the configured listen address.
// Returns the actual address (useful when port 8787 is busy and we fall back).
func (a *App) StartProxy() (string, error) {
        settings := a.store.Settings()
        if !settings.ProxyEnabled {
                return "", errors.New("proxy disabled in settings")
        }
        listen := settings.ProxyListen
        if listen == "" {
                listen = "127.0.0.1:8787"
        }
        if err := a.proxy.Start(listen); err != nil {
                fallback := "127.0.0.1:8788"
                if err2 := a.proxy.Start(fallback); err2 != nil {
                        return "", fmt.Errorf("listen on %s: %v (fallback %s: %v)", listen, err, fallback, err2)
                }
                _ = a.store.UpdateSettings(func(s *store.Settings) { s.ProxyListen = fallback })
                return a.proxy.Addr(), nil
        }
        return a.proxy.Addr(), nil
}

// StopProxy shuts down the HTTP proxy.
func (a *App) StopProxy(ctx context.Context) error { return a.proxy.Stop(ctx) }

// ProxyAddr returns the address the proxy is listening on, or "" if not running.
func (a *App) ProxyAddr() string { return a.proxy.Addr() }

// ---------- Models ----------

// ModelInfo exposes upstream.ModelInfo without leaking the upstream type.
type ModelInfo = upstream.ModelInfo

// ListModels asks the upstream for available models.
func (a *App) ListModels(ctx context.Context) ([]ModelInfo, error) {
        token, _, settings, err := a.ensureCreds(ctx)
        if err != nil {
                return nil, err
        }
        return a.upstream.ListModels(ctx, token, settings)
}

// ---------- Chat ----------

// ChatMessage is a single chat turn.
type ChatMessage = upstream.ChatMessage

// ChatEvent is a streamed event from the upstream chat API.
type ChatEvent = upstream.StreamEvent

// ChatUsage is the token usage summary.
type ChatUsage = upstream.Usage

// ChatOptions configures a StreamChat call.
type ChatOptions struct {
        Model       string
        Effort      string
        NoThinking  bool
        Temperature float64
        MaxTokens   int
}

// StreamChat runs a streaming chat turn against the active account using the
// Responses API (matching the desktop app behaviour).
func (a *App) StreamChat(ctx context.Context, messages []ChatMessage, emit func(ChatEvent)) error {
        return a.StreamChatWithOptions(ctx, messages, ChatOptions{}, emit)
}

// StreamChatWithOptions runs a streaming chat turn with the given options.
func (a *App) StreamChatWithOptions(
        ctx context.Context,
        messages []ChatMessage,
        opts ChatOptions,
        emit func(ChatEvent),
) error {
        token, acc, settings, err := a.ensureCreds(ctx)
        if err != nil {
                return err
        }
        req := upstream.ChatRequest{
                Messages: messages,
                Stream:   true,
                APIMode:  "responses",
        }
        if opts.Model != "" {
                req.Model = opts.Model
        } else {
                req.Model = settings.DefaultModel
        }
        if opts.Effort != "" {
                req.ReasoningEffort = opts.Effort
        } else {
                req.ReasoningEffort = settings.ReasoningEffort
        }
        if opts.NoThinking {
                req.ReasoningEffort = "low"
        }
        if opts.Temperature > 0 {
                req.Temperature = opts.Temperature
        }
        if opts.MaxTokens > 0 {
                req.MaxTokens = opts.MaxTokens
        }
        label := acc.Label
        if label == "" {
                label = acc.Email
        }
        wrap := func(ev ChatEvent) {
                if ev.Account == "" {
                        ev.Account = label
                }
                if ev.Email == "" {
                        ev.Email = acc.Email
                }
                if ev.Type == "usage" && ev.Usage != nil {
                        model := ev.Model
                        if model == "" {
                                model = req.Model
                        }
                        cost := pricing.CostUSD(model, ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.ReasoningTokens, ev.Usage.CachedTokens)
                        total := ev.Usage.TotalTokens
                        if total == 0 {
                                total = ev.Usage.PromptTokens + ev.Usage.CompletionTokens
                        }
                        sample := store.RequestSample{
                                ID:               ev.ID,
                                At:               time.Now().UTC().Format(time.RFC3339),
                                AccountID:        acc.ID,
                                Model:            model,
                                PromptTokens:     ev.Usage.PromptTokens,
                                CompletionTokens: ev.Usage.CompletionTokens,
                                ReasoningTokens:  ev.Usage.ReasoningTokens,
                                CachedTokens:     ev.Usage.CachedTokens,
                                TotalTokens:      total,
                                CostUSD:          cost,
                                LatencyMs:        ev.LatencyMs,
                                TTFTMs:           ev.TTFTMs,
                                Estimated:        ev.Estimated,
                        }
                        _ = a.store.RecordRequest(sample)
                }
                emit(ev)
        }
        return a.upstream.StreamChat(ctx, token, settings, label, acc.Email, req, wrap)
}

// ---------- internal ----------

func (a *App) ensureCreds(ctx context.Context) (string, *store.Account, store.Settings, error) {
        settings := a.store.Settings()
        acc, ok := a.store.ActiveAccount()
        if !ok || acc == nil {
                return "", nil, settings, errors.New("nenhuma conta — execute `grok-proxy-cli login`")
        }
        if acc.ExpiresSoon(5*time.Minute) && acc.RefreshToken != "" {
                tok, err := a.oauth.Refresh(ctx, acc.RefreshToken, acc.ClientID, acc.Issuer)
                if err != nil {
                        if acc.Expired() {
                                return "", nil, settings, fmt.Errorf("token expirado — faça login de novo: %v", err)
                        }
                } else {
                        acc.AccessToken = tok.AccessToken
                        if tok.RefreshToken != "" {
                                acc.RefreshToken = tok.RefreshToken
                        }
                        acc.ExpiresAt = time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
                        acc.UpdatedAt = time.Now().UTC()
                        _ = a.store.UpsertAccount(*acc)
                }
        }
        if acc.AccessToken == "" {
                return "", nil, settings, errors.New("conta sem access_token")
        }
        return acc.AccessToken, acc, settings, nil
}
