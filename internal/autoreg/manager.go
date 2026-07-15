// Package autoreg orchestrates automatic Grok account registration using a
// temporary email address to receive the verification email.
//
// The flow is:
//
//  1. Provision a temp-mail inbox (mail.tm or tempmail.lol)
//  2. Kick off xAI's OAuth device-code flow (existing oauth.Client)
//  3. Open a headless browser session at x.ai's signup page using the device
//     code URL, fill the temp email, submit, and wait for the verification
//     email to arrive
//  4. Extract the verification link from the email body
//  5. Hit the verification link to confirm the email
//  6. Complete the device-code poll to receive the access + refresh tokens
//  7. Save the new account to the store
//
// NOTE on the signup automation: x.ai/Grok uses a browser-based signup that
// may include CAPTCHAs, Proof-of-Work, fingerprinting, and CSRF tokens that
// make pure-HTTP automation fragile. The implementation below supports two
// modes:
//
//   - "assisted" (default): emits the device-code URL + verification code and
//     the temp email so a human can complete the signup in a browser while
//     the program waits for the email and OAuth poll. This is the most
//     reliable mode.
//   - "automated": attempts to script the signup via HTTP requests. May break
//     if x.ai changes the flow or presents a CAPTCHA.
package autoreg

import (
        "context"
        "errors"
        "fmt"
        "net/http"
        "sync"
        "time"

        "grok-desktop/internal/oauth"
        "grok-desktop/internal/store"
        "grok-desktop/internal/tempmail"
)

// Options controls the behaviour of Manager.Register.
type Options struct {
        // ProviderName selects which temp-mail provider to use ("mail.tm" or
        // "tempmail.lol"). Empty = "mail.tm".
        ProviderName string

        // FromFilter is the substring matched against the sender address to
        // identify the verification email. Defaults to "x.ai".
        FromFilter string

        // EmailWaitTimeout is how long to wait for the verification email.
        // Defaults to 5 minutes.
        EmailWaitTimeout time.Duration

        // SignupTimeout is the overall timeout for the entire flow.
        // Defaults to 10 minutes.
        SignupTimeout time.Duration

        // CleanupInbox controls whether the temp-mail inbox is deleted after
        // the flow completes (success or failure).
        CleanupInbox bool

        // AutomatedSignup toggles whether the Playwright browser is used to
        // walk the x.ai signup. False (default) uses assisted mode where the
        // user opens a browser manually.
        AutomatedSignup bool

        // Headed, when true, shows the Chromium window during the signup
        // (useful for debugging the Playwright automation). Defaults to false
        // (headless).
        Headed bool

        // HTTPClient is optional; if nil, http.DefaultClient is used.
        HTTPClient *http.Client

        // Logger is an optional sink for progress messages.
        Logger func(format string, args ...any)
}

func (o *Options) normalize() {
        if o.ProviderName == "" {
                o.ProviderName = "mail.tm"
        }
        if o.FromFilter == "" {
                o.FromFilter = "x.ai"
        }
        if o.EmailWaitTimeout <= 0 {
                o.EmailWaitTimeout = 5 * time.Minute
        }
        if o.SignupTimeout <= 0 {
                o.SignupTimeout = 10 * time.Minute
        }
        if o.Logger == nil {
                o.Logger = func(string, ...any) {}
        }
}

// Result is the outcome of a successful Register call.
type Result struct {
        Account  store.Account
        Inbox    *tempmail.Inbox
        Provider tempmail.Provider
}

// Manager coordinates the OAuth client, store, and temp-mail provider.
type Manager struct {
        mu    sync.Mutex
        oauth *oauth.Client
        store *store.Store
}

// New returns a Manager that uses the given OAuth client and store.
func New(o *oauth.Client, s *store.Store) *Manager {
        if o == nil {
                o = oauth.New()
        }
        return &Manager{oauth: o, store: s}
}

// Register performs the full auto-register flow.
//
// The assisted flow (default) goes:
//
//  1. Provision a temp inbox (Provider.CreateInbox)
//  2. Start the device-code login (oauth.StartDevice)
//  3. Print/log the verification URL + user code + temp email so the caller
//     can open a browser and complete the signup using that email
//  4. In parallel:
//       a) Wait for the verification email (Provider.WaitForMessage)
//       b) Hit the verification link to confirm
//       c) Poll the device-code endpoint until the user finishes
//  5. On success, save the account to the store and mark it active
//  6. Optionally delete the temp inbox
func (m *Manager) Register(ctx context.Context, opts Options) (*Result, error) {
        opts.normalize()
        overallCtx, cancel := context.WithTimeout(ctx, opts.SignupTimeout)
        defer cancel()

        // 1. Provision temp inbox
        opts.Logger("autoreg: provisionando inbox temporário (%s)", opts.ProviderName)
        provider, err := tempmail.SelectProvider(opts.ProviderName)
        if err != nil {
                return nil, fmt.Errorf("selecionar provedor: %w", err)
        }
        inbox, err := provider.CreateInbox(overallCtx)
        if err != nil {
                return nil, fmt.Errorf("criar inbox: %w", err)
        }
        opts.Logger("autoreg: inbox criado: %s (provedor %s)", inbox.Address, provider.Name())

        cleanup := func() {
                if !opts.CleanupInbox {
                        return
                }
                ctx2, c := context.WithTimeout(context.Background(), 10*time.Second)
                defer c()
                _ = provider.DeleteInbox(ctx2, inbox)
        }
        defer cleanup()

        // 2. Start device-code login
        opts.Logger("autoreg: iniciando device-code flow no x.ai")
        start, err := m.oauth.StartDevice(overallCtx)
        if err != nil {
                return nil, fmt.Errorf("start device: %w", err)
        }
        verifyURL := start.VerificationURIComplete
        if verifyURL == "" {
                verifyURL = start.VerificationURI
        }
        opts.Logger("autoreg: abra no navegador: %s", verifyURL)
        opts.Logger("autoreg: código do dispositivo: %s", start.UserCode)
        opts.Logger("autoreg: use este email no signup: %s", inbox.Address)

        // 3. Run the signup flow.
        //
        // Two modes:
        //   - Automated: launch Playwright/Chromium, fill the email, click
        //     through the form, wait for the verification email, open the
        //     verification link in the same browser session. This blocks
        //     until the verification step is complete (or fails).
        //   - Assisted (default): print the URL + email + code so the user
        //     can complete the signup in their own browser. We then wait
        //     for the verification email and hit the link via plain HTTP.
        if opts.AutomatedSignup {
                opts.Logger("autoreg: iniciando signup automatizado via Playwright")
                if err := m.playwrightSignup(overallCtx, inbox, verifyURL, start.UserCode, provider, opts); err != nil {
                        return nil, fmt.Errorf("signup automatizado: %w", err)
                }
        } else {
                // Assisted mode: wait for the email then hit the verify link.
                opts.Logger("autoreg: modo assistido — abra o navegador e complete o signup manualmente")
                opts.Logger("autoreg: aguardando email de verificação…")
                msg, err := provider.WaitForMessage(overallCtx, inbox.Token, opts.FromFilter, opts.EmailWaitTimeout)
                if err != nil {
                        return nil, fmt.Errorf("aguardar email: %w", err)
                }
                opts.Logger("autoreg: email recebido de %s — assunto: %q", msg.From, msg.Subject)
                link := tempmail.ExtractVerificationLink(msg)
                if link == "" {
                        return nil, errors.New("nenhum link de verificação encontrado no email")
                }
                opts.Logger("autoreg: confirmando link: %s", link)
                if err := m.confirmLink(overallCtx, link, opts.HTTPClient); err != nil {
                        opts.Logger("autoreg: aviso confirmando link: %v", err)
                }
        }

        // 6. Poll the device-code endpoint until tokens come back.
        opts.Logger("autoreg: aguardando tokens OAuth…")
        tok, err := m.oauth.PollDevice(overallCtx, start.DeviceCode, start.Interval)
        if err != nil {
                return nil, fmt.Errorf("poll device: %w", err)
        }
        acc := oauth.AccountFromToken(tok, m.oauth.ClientID, m.oauth.Issuer)
        email, uid := m.oauth.UserInfo(overallCtx, tok.AccessToken, m.oauth.Issuer)
        if email != "" {
                acc.Email = email
        }
        if uid != "" {
                acc.UserID = uid
                acc.ID = uid
        }
        if acc.Email == "" {
                acc.Email = inbox.Address
        }
        if acc.Label == "" || acc.Label == "Grok account" {
                acc.Label = inbox.Address
        }
        opts.Logger("autoreg: conta obtida: id=%s email=%s", acc.ID, acc.Email)

        // 7. Save & activate.
        if err := m.store.UpsertAccount(acc); err != nil {
                return nil, fmt.Errorf("salvar conta: %w", err)
        }
        _ = m.store.SetActiveAccount(acc.ID)

        return &Result{Account: acc, Inbox: inbox, Provider: provider}, nil
}

// confirmLink does a single GET request against the verification URL. x.ai's
// verify endpoint is typically idempotent — hitting it once marks the email
// as confirmed.
func (m *Manager) confirmLink(ctx context.Context, link string, hc *http.Client) error {
        if hc == nil {
                hc = &http.Client{Timeout: 30 * time.Second}
        }
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
        if err != nil {
                return err
        }
        // Mimic a browser so the server doesn't 403 the verify request.
        req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
        req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
        req.Header.Set("Accept-Language", "en-US,en;q=0.9")
        resp, err := hc.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode >= 400 {
                return fmt.Errorf("verify link HTTP %d", resp.StatusCode)
        }
        return nil
}

// tryAutomatedSignup was the previous HTTP-based automation stub. It has been
// replaced by playwrightSignup (see playwright_signup.go). The function is
// kept here as an explicit tombstone so callers reading manager.go understand
// where to look for the real implementation.
//
// Deprecated: use playwrightSignup via Options.AutomatedSignup=true.
func (m *Manager) tryAutomatedSignup(ctx context.Context, email, verifyURL, userCode string, opts Options) error {
        _ = ctx
        _ = email
        _ = verifyURL
        _ = userCode
        _ = opts
        return errors.New("tryAutomatedSignup foi substituído por playwrightSignup — use Options.AutomatedSignup=true")
}
