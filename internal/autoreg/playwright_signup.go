// playwright_signup.go implements the automated Grok signup using a headless
// Chromium controlled via Playwright. This is far more reliable than pure HTTP
// because x.ai's signup page is JavaScript-heavy, uses anti-bot fingerprinting
// and may present interactive challenges.
//
// Flow:
//
//  1. Launch headless Chromium with a fresh context (no cookies).
//  2. Navigate to the device-code verification URL (verification_uri_complete).
//  3. The x.ai page shows the pre-filled user code and asks the user to sign
//     in or sign up. We click "Sign up" / "Create account".
//  4. Fill the temp email address into the email field.
//  5. Continue through any password / consent screens (best-effort — x.ai
//     changes these often).
//  6. In parallel, poll the temp inbox for the verification email.
//  7. When the email arrives, extract the verification link and open it in
//     THE SAME browser context so the session cookies match. This is the key
//     advantage over the HTTP confirm: the x.ai backend ties the verification
//     to the browser session that initiated the signup.
//  8. After the verify page resolves, x.ai redirects back to the device-code
//     flow which completes the OAuth grant. The Go-side PollDevice then
//     receives the access & refresh tokens.
//
// If anything goes wrong, we fall back to assisted mode (print URL + email +
// code so the user can finish manually in a browser).
package autoreg

import (
        "context"
        "errors"
        "fmt"
        "strings"
        "time"

        playwright "github.com/mxschmitt/playwright-go"

        "grok-desktop/internal/tempmail"
)

// playwrightSignup runs the automated signup using Playwright/Chromium.
//
// It does NOT block on the OAuth poll — that's the caller's job. It returns
// once the browser has either completed the verification step or hit an error
// that requires human intervention.
func (m *Manager) playwrightSignup(
        ctx context.Context,
        inbox *tempmail.Inbox,
        verifyURL string,
        userCode string,
        provider tempmail.Provider,
        opts Options,
) error {
        // Make sure Playwright browsers are installed. If not, install them now.
        if err := ensurePlaywrightInstalled(); err != nil {
                return fmt.Errorf("instalar playwright: %w", err)
        }

        pw, err := playwright.Run()
        if err != nil {
                return fmt.Errorf("playwright run: %w", err)
        }
        defer pw.Stop()

        // Use headless Chromium (unless --headed was passed). Set a realistic
        // User-Agent and viewport so x.ai's anti-bot doesn't immediately flag
        // the session.
        headless := !opts.Headed
        browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
                Headless: playwright.Bool(headless),
                Args: []string{
                        "--no-sandbox",
                        "--disable-blink-features=AutomationControlled",
                        "--disable-dev-shm-usage",
                },
        })
        if err != nil {
                return fmt.Errorf("launch chromium: %w", err)
        }
        defer browser.Close()

        // Fresh context with realistic fingerprint.
        browserCtx, err := browser.NewContext(playwright.BrowserNewContextOptions{
                UserAgent: playwright.String("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"),
                Viewport: &playwright.Size{
                        Width:  1280,
                        Height: 800,
                },
                Locale:            playwright.String("en-US"),
                TimezoneId:        playwright.String("America/Sao_Paulo"),
                IgnoreHttpsErrors: playwright.Bool(true),
        })
        if err != nil {
                return fmt.Errorf("new context: %w", err)
        }
        defer browserCtx.Close()

        page, err := browserCtx.NewPage()
        if err != nil {
                return fmt.Errorf("new page: %w", err)
        }
        defer page.Close()

        // Mask the navigator.webdriver flag (basic anti-bot evasion).
        if err := page.AddInitScript(playwright.Script{
                Content: playwright.String(`Object.defineProperty(navigator, 'webdriver', {get: () => undefined});`),
        }); err != nil {
                opts.Logger("autoreg/playwright: aviso addInitScript: %v", err)
        }

        // 1. Navigate to the verification URL.
        opts.Logger("autoreg/playwright: navegando para %s", verifyURL)
        if _, err := page.Goto(verifyURL, playwright.PageGotoOptions{
                WaitUntil: playwright.WaitUntilStateNetworkidle,
                Timeout:   playwright.Float(60000),
        }); err != nil {
                return fmt.Errorf("goto: %w", err)
        }

        // 1b. Detect Cloudflare / anti-bot challenge. If the page body contains
        //     "Blocked due to abusive traffic patterns" or "Just a moment"
        //     (the Cloudflare interstitial), we know we cannot proceed with
        //     automated signup from this IP. Wait briefly — sometimes the
        //     challenge resolves in 5-15s — but if it doesn't, fall back to
        //     assisted mode by printing the URL + email + code so the user
        //     can complete the signup in their own browser (which is on a
        //     different IP and won't be flagged).
        cloudflareDetected := false
        for i := 0; i < 6; i++ {
                bodyText, _ := page.InnerText("body")
                if bodyText == "" {
                        time.Sleep(2 * time.Second)
                        continue
                }
                low := strings.ToLower(bodyText)
                if strings.Contains(low, "blocked due to abusive") ||
                        strings.Contains(low, "just a moment") ||
                        strings.Contains(low, "checking your browser") ||
                        strings.Contains(low, "enable javascript and cookies") {
                        cloudflareDetected = true
                        opts.Logger("autoreg/playwright: Cloudflare/anti-bot detectado (tentativa %d/6)", i+1)
                        time.Sleep(5 * time.Second)
                        continue
                }
                cloudflareDetected = false
                break
        }
        if cloudflareDetected {
                opts.Logger("autoreg/playwright: bloqueio Cloudflare persistente — IP de datacenter provavelmente flaggado")
                opts.Logger("autoreg/playwright: caindo para modo assistido")
                opts.Logger("")
                opts.Logger("========================================================")
                opts.Logger("MODO ASSISTIDO (devido a bloqueio Cloudflare)")
                opts.Logger("========================================================")
                opts.Logger("1. Abra no navegador (de preferência em IP residencial):")
                opts.Logger("   %s", verifyURL)
                opts.Logger("2. Código do dispositivo: %s", userCode)
                opts.Logger("3. Use este email temporário no signup:")
                opts.Logger("   %s", inbox.Address)
                opts.Logger("4. Complete o signup manualmente.")
                opts.Logger("   O programa vai aguardar o email de verificação e")
                opts.Logger("   completar o poll OAuth automaticamente.")
                opts.Logger("========================================================")
                // Now fall through to the email-wait step below, which will block
                // until the verification email arrives (i.e. the user completed
                // the signup in their browser).
                msg, err := provider.WaitForMessage(ctx, inbox.Token, opts.FromFilter, opts.EmailWaitTimeout)
                if err != nil {
                        return fmt.Errorf("aguardar email (modo assistido): %w", err)
                }
                opts.Logger("autoreg/playwright: email recebido de %s — assunto: %q", msg.From, msg.Subject)
                link := tempmail.ExtractVerificationLink(msg)
                if link == "" {
                        return errors.New("nenhum link de verificação encontrado no email")
                }
                opts.Logger("autoreg/playwright: confirmando link via HTTP: %s", link)
                // Use the HTTP confirm (no need to open in browser — the user
                // already clicked it themselves OR the link is idempotent).
                if err := m.confirmLink(ctx, link, nil); err != nil {
                        opts.Logger("autoreg/playwright: aviso confirmando link: %v", err)
                }
                return nil
        }

        // 2. Wait briefly for the page to render the signup/login choice.
        time.Sleep(2 * time.Second)

        // 3. Try to find a "Sign up" / "Create account" link/button. x.ai's
        //    device-code page typically lands directly on a login form with a
        //    "Sign up" link. We try multiple selectors.
        signupClicked := false
        signupSelectors := []string{
                "a:has-text('Sign up')",
                "a:has-text('Sign Up')",
                "a:has-text('sign up')",
                "button:has-text('Sign up')",
                "button:has-text('Create account')",
                "a:has-text('Create account')",
                "a:has-text('Register')",
        }
        for _, sel := range signupSelectors {
                if locator := page.Locator(sel); locator != nil {
                        count, _ := locator.Count()
                        if count > 0 {
                                if err := locator.First().Click(playwright.LocatorClickOptions{
                                        Timeout: playwright.Float(5000),
                                }); err == nil {
                                        signupClicked = true
                                        opts.Logger("autoreg/playwright: clicou em signup (%s)", sel)
                                        break
                                }
                        }
                }
        }
        if !signupClicked {
                opts.Logger("autoreg/playwright: nenhum botão de signup encontrado — talvez já esteja no formulário")
        }

        // 4. Fill the email field. We try several selectors because x.ai's
        //    signup form changes between deployments.
        time.Sleep(1500 * time.Millisecond)
        emailFilled := false
        emailSelectors := []string{
                "input[type='email']",
                "input[name='email']",
                "input[id*='email' i]",
                "input[placeholder*='email' i]",
                "input[autocomplete='email']",
        }
        for _, sel := range emailSelectors {
                if locator := page.Locator(sel); locator != nil {
                        count, _ := locator.Count()
                        if count > 0 {
                                if err := locator.First().Fill(inbox.Address, playwright.LocatorFillOptions{
                                        Timeout: playwright.Float(5000),
                                }); err == nil {
                                        emailFilled = true
                                        opts.Logger("autoreg/playwright: email preenchido (%s)", sel)
                                        break
                                }
                        }
                }
        }
        if !emailFilled {
                return errors.New("não foi possível encontrar o campo de email — pode ser necessário modo assistido")
        }

        // 5. Click "Continue" / "Next" / "Sign up" to submit the email.
        submitSelectors := []string{
                "button:has-text('Continue')",
                "button:has-text('Next')",
                "button:has-text('Sign up')",
                "button:has-text('Sign Up')",
                "button[type='submit']",
        }
        for _, sel := range submitSelectors {
                if locator := page.Locator(sel); locator != nil {
                        count, _ := locator.Count()
                        if count > 0 {
                                if err := locator.First().Click(playwright.LocatorClickOptions{
                                        Timeout: playwright.Float(5000),
                                }); err == nil {
                                        opts.Logger("autoreg/playwright: clicou em submit (%s)", sel)
                                        break
                                }
                        }
                }
        }

        // 6. Wait for either a CAPTCHA / password / verification prompt.
        //    At this point x.ai typically sends the verification email.
        opts.Logger("autoreg/playwright: aguardando email de verificação…")
        msg, err := provider.WaitForMessage(ctx, inbox.Token, opts.FromFilter, opts.EmailWaitTimeout)
        if err != nil {
                return fmt.Errorf("aguardar email: %w", err)
        }
        opts.Logger("autoreg/playwright: email recebido de %s — assunto: %q", msg.From, msg.Subject)

        // 7. Extract the verification link.
        link := tempmail.ExtractVerificationLink(msg)
        if link == "" {
                return errors.New("nenhum link de verificação encontrado no email")
        }
        opts.Logger("autoreg/playwright: abrindo link de verificação no browser: %s", link)

        // 8. Open the verification link in the SAME browser context (so cookies
        //    match the session that initiated the signup). Use a new page so we
        //    don't lose the original.
        verifyPage, err := browserCtx.NewPage()
        if err != nil {
                return fmt.Errorf("new verify page: %w", err)
        }
        defer verifyPage.Close()

        if _, err := verifyPage.Goto(link, playwright.PageGotoOptions{
                WaitUntil: playwright.WaitUntilStateNetworkidle,
                Timeout:   playwright.Float(60000),
        }); err != nil {
                return fmt.Errorf("goto verify: %w", err)
        }

        // Wait a bit for any post-verify redirect.
        time.Sleep(3 * time.Second)
        finalURL := verifyPage.URL()
        opts.Logger("autoreg/playwright: URL final após verificação: %s", finalURL)

        // 9. Look for a confirmation message on the page (best-effort).
        bodyText, _ := verifyPage.InnerText("body")
        if bodyText == "" {
                bodyText = ""
        }
        if strings.Contains(strings.ToLower(bodyText), "verified") ||
                strings.Contains(strings.ToLower(bodyText), "confirm") ||
                strings.Contains(strings.ToLower(bodyText), "success") ||
                strings.Contains(strings.ToLower(bodyText), "verif") {
                opts.Logger("autoreg/playwright: verificação parece ter sido concluída")
                return nil
        }

        // Even if we can't detect a success message, the device-code poll on the
        // caller side will tell us definitively whether the OAuth grant completed.
        opts.Logger("autoreg/playwright: não foi possível confirmar sucesso pela página — dependerá do poll OAuth")
        return nil
}

// ensurePlaywrightInstalled installs the Chromium browser binaries if they
// are not already present. This is a no-op if they are already installed.
var (
        playwrightInstalled     = false
        playwrightInstallFailed = ""
)

func ensurePlaywrightInstalled() error {
        if playwrightInstalled {
                return nil
        }
        if playwrightInstallFailed != "" {
                return errors.New(playwrightInstallFailed)
        }
        if err := playwright.Install(); err != nil {
                playwrightInstallFailed = err.Error()
                return err
        }
        playwrightInstalled = true
        return nil
}
