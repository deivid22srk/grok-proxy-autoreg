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
// This is the "pure assisted" flow (no Playwright, no headless browser):
//
//  1. Provision a temp inbox (Provider.CreateInbox)
//  2. Start the device-code login (oauth.StartDevice)
//  3. Print the verification URL + user code + temp email so the caller
//     can open a browser and complete the signup using that email
//  4. Poll the temp inbox until the verification email arrives
//  5. Extract the verification link from the email body and:
//     - Print it loudly so the user can click it manually if needed
//     - Also hit it via HTTP (idempotent — works for most x.ai verify links)
//  6. Poll the device-code endpoint until the OAuth grant completes
//  7. Save the new account to the store and mark it active
//  8. Optionally delete the temp inbox
//
// The user only needs to do two things in their browser:
//   - Open the URL printed by step 3
//   - Use the printed temp email when asked by x.ai
//   - Click the verification link that x.ai emails (step 5 prints it again
//     just in case the HTTP confirm in step 5 doesn't trigger the grant)
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

        // 3. Print a very clear banner for the user.
        opts.Logger("")
        opts.Logger("================================================================")
        opts.Logger("  PASSO 1 — Abra esta URL no seu navegador:")
        opts.Logger("  %s", verifyURL)
        opts.Logger("----------------------------------------------------------------")
        opts.Logger("  PASSO 2 — Quando o x.ai pedir o email, use EXATAMENTE:")
        opts.Logger("  %s", inbox.Address)
        opts.Logger("----------------------------------------------------------------")
        opts.Logger("  Código do dispositivo (se pedir): %s", start.UserCode)
        opts.Logger("----------------------------------------------------------------")
        opts.Logger("  O programa está monitorando o inbox e vai detectar o email")
        opts.Logger("  de verificação automaticamente. Assim que chegar, ele vai")
        opts.Logger("  imprimir o link de confirmação.")
        opts.Logger("================================================================")
        opts.Logger("")

        // 4. Wait for the verification email.
        opts.Logger("autoreg: aguardando email de verificação (timeout %s)…", opts.EmailWaitTimeout)
        msg, err := provider.WaitForMessage(overallCtx, inbox.Token, opts.FromFilter, opts.EmailWaitTimeout)
        if err != nil {
                return nil, fmt.Errorf("aguardar email: %w", err)
        }
        opts.Logger("autoreg: ✓ email recebido de %s — assunto: %q", msg.From, msg.Subject)

        // 5. Extract & display the verification link.
        link := tempmail.ExtractVerificationLink(msg)
        if link == "" {
                return nil, errors.New("nenhum link de verificação encontrado no email")
        }
        opts.Logger("")
        opts.Logger("================================================================")
        opts.Logger("  PASSO 3 — Email de verificação recebido!")
        opts.Logger("----------------------------------------------------------------")
        opts.Logger("  Link de confirmação:")
        opts.Logger("  %s", link)
        opts.Logger("----------------------------------------------------------------")
        opts.Logger("  Tentando confirmar automaticamente via HTTP…")
        opts.Logger("  (se o OAuth não completar em ~30s, abra o link acima")
        opts.Logger("   no navegador onde você fez o signup)")
        opts.Logger("================================================================")
        opts.Logger("")

        // Best-effort HTTP confirm — works if the verify endpoint is idempotent
        // and doesn't tie the click to the browser session.
        confirmErr := m.confirmLink(overallCtx, link, opts.HTTPClient)
        if confirmErr != nil {
                opts.Logger("autoreg: aviso — confirmLink HTTP falhou: %v", confirmErr)
                opts.Logger("autoreg: isso é normal se o x.ai exigir clique no browser — abra o link acima manualmente")
        } else {
                opts.Logger("autoreg: ✓ link confirmado via HTTP")
        }

        // 6. Poll the device-code endpoint until tokens come back.
        opts.Logger("autoreg: aguardando tokens OAuth (poll a cada %ds)…", start.Interval)
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
        opts.Logger("autoreg: ✓ conta obtida: id=%s email=%s", acc.ID, acc.Email)

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
