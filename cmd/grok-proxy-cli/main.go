// Command grok-proxy-cli is a terminal-only build of Grok Proxy Plus.
//
// It does NOT depend on Wails or any GUI toolkit, so it can be built and
// run from a single command on Linux, macOS, and Windows without a
// desktop environment. It supports:
//
//   - `login`      : start the OAuth device-code flow and save the account
//   - `accounts`   : list configured accounts
//   - `use <id>`   : set the active account
//   - `logout <id>`: remove an account
//   - `models`     : list upstream models
//   - `chat`       : interactive REPL with streaming + thinking
//   - `serve`      : (default) start the local OpenAI-compatible proxy
//   - `ask <text>` : one-shot prompt from the command line
//   - `rotate`     : show rotation status / manually rotate to next account
//
// All state (accounts, settings, usage, history) is stored under the same
// AppData directory used by the desktop app, so the two builds can share
// credentials.
package main

import (
        "bufio"
        "context"
        "flag"
        "fmt"
        "os"
        "os/signal"
        "path/filepath"
        "strings"
        "syscall"
        "time"

        "grok-desktop/internal/app"
        "grok-desktop/internal/store"
)

const version = "1.1.0-cli"

func main() {
        if len(os.Args) < 2 {
                runServe(nil)
                return
        }

        cmd := os.Args[1]
        args := os.Args[2:]

        switch cmd {
        case "-h", "--help", "help":
                printHelp()
                return
        case "-v", "--version", "version":
                fmt.Println("grok-proxy-cli", version)
                return
        case "serve":
                runServe(args)
        case "login":
                runLogin(args)
        case "logout":
                runLogout(args)
        case "accounts":
                runAccounts(args)
        case "use":
                runUse(args)
        case "models":
                runModels(args)
        case "chat":
                runChat(args)
        case "ask":
                runAsk(args)
        case "rotate":
                runRotate(args)
        case "autoreg":
                runAutoReg(args)
        default:
                fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
                printHelp()
                os.Exit(2)
        }
}

func printHelp() {
        fmt.Printf(`grok-proxy-cli %s — terminal-only Grok proxy with auto-rotation
and automatic account registration via temporary email + Playwright.

Usage:
  grok-proxy-cli                       start the local OpenAI proxy (default = serve)
  grok-proxy-cli serve                 same as above; flags: --listen, --api-key, --no-proxy,
                                       --no-rotate, --rotate-verbose, --auto-reg, --no-auto-reg,
                                       --provider <mail.tm|tempmail.lol>, --headed
  grok-proxy-cli login                 sign in with xAI device-code OAuth (manual)
  grok-proxy-cli autoreg               create a new account automatically using a temp inbox
                                       and Playwright-driven signup; flags: --provider,
                                       --no-browser (assisted mode), --headed, --keep-inbox
  grok-proxy-cli accounts              list accounts
  grok-proxy-cli use <id>              set active account (id prefix supported)
  grok-proxy-cli logout <id>           remove an account
  grok-proxy-cli models                list models available to the active account
  grok-proxy-cli chat                  interactive streaming chat REPL
  grok-proxy-cli ask "<prompt>"        one-shot prompt; flags: --effort, --model, --no-think
  grok-proxy-cli rotate                show rotation status; flags: --next, --reset <id>, --reset-all

Global flags (any command):
  --data-dir <path>                    override AppData directory (default: %s)

Proxy base URL once running:
  http://127.0.0.1:8787/v1

Endpoints:
  GET  /v1/models
  POST /v1/chat/completions
  POST /v1/responses
  POST /v1/messages

Auto-rotation:
  When a request hits HTTP 429 (rate limit) or 402 (payment required) from
  xAI, the proxy automatically marks the active account as limited, switches
  to the next available account, and retries the request. Limited accounts
  re-enter the pool after a cooldown (5 min for rate limits, 6 h for quotas).

Auto-registration (NEW):
  When --auto-reg is on (default in 'serve') and ALL accounts are limited,
  the proxy provisions a fresh temp email (mail.tm or tempmail.lol), opens
  a headless Chromium via Playwright, walks the x.ai signup using that email,
  receives the verification email, opens the verification link in the same
  browser session, completes the OAuth device-code flow and saves the new
  account to the store — then transparently uses it for the retried request.
  Use --no-auto-reg to disable; --headed to watch the browser; --provider to
  pick the temp-mail backend.
`, version, defaultDataDirHint())
}

func defaultDataDirHint() string {
        dir, err := store.DefaultDataDir()
        if err != nil {
                return "<user-appdata>/GrokDesktop"
        }
        return dir
}

// ---- shared helpers ----

func mustApp() *app.App {
        a, err := app.Open("")
        if err != nil {
                fail("open store: %v", err)
        }
        return a
}

func fail(format string, args ...any) {
        fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
        os.Exit(1)
}

// ---- commands ----

func runServe(args []string) {
        fs := flag.NewFlagSet("serve", flag.ExitOnError)
        listen := fs.String("listen", "", "override listen address (default 127.0.0.1:8787)")
        apiKey := fs.String("api-key", "", "require this API key on requests")
        noProxy := fs.Bool("no-proxy", false, "do not start the HTTP proxy")
        noRotate := fs.Bool("no-rotate", false, "disable automatic account rotation on rate-limit")
        rotateVerbose := fs.Bool("rotate-verbose", false, "log every account rotation to stderr")
        autoReg := fs.Bool("auto-reg", true, "automatically register a fresh account when all accounts are limited (default ON)")
        provider := fs.String("provider", "mail.tm", "temp-mail provider: mail.tm | tempmail.lol")
        headed := fs.Bool("headed", false, "show the Chromium window during auto-registration (debug)")
        keepInbox := fs.Bool("keep-inbox", false, "do not delete the temp inbox after auto-registration")
        emailWait := fs.Duration("email-wait", 5*time.Minute, "how long to wait for the verification email")
        signupTimeout := fs.Duration("signup-timeout", 10*time.Minute, "overall timeout for the auto-registration flow")
        _ = fs.Parse(args)

        a := mustApp()
        defer a.Close()

        if *apiKey != "" {
                _ = a.UpdateSettings(func(s *store.Settings) { s.ProxyAPIKey = *apiKey })
        }
        if *listen != "" {
                _ = a.UpdateSettings(func(s *store.Settings) { s.ProxyListen = *listen; s.ProxyEnabled = true })
        }
        a.SetAutoRotate(!*noRotate)
        a.SetRotatorVerbose(*rotateVerbose)

        // Wire up auto-registration.
        a.SetAutoReg(*autoReg, app.AutoRegOptions{
                ProviderName:     *provider,
                EmailWaitTimeout: *emailWait,
                SignupTimeout:    *signupTimeout,
                CleanupInbox:     !*keepInbox,
                AutomatedSignup:  true, // Playwright mode (the whole point)
                Headed:           *headed,
                Logger: func(format string, args ...any) {
                        fmt.Fprintf(os.Stderr, "[autoreg] "+format+"\n", args...)
                },
        })

        if !*noProxy {
                addr, err := a.StartProxy()
                if err != nil {
                        fail("start proxy: %v", err)
                }
                fmt.Printf("grok-proxy-plus listening on http://%s/v1\n", addr)
                fmt.Println("endpoints:")
                fmt.Println("  GET  /v1/models")
                fmt.Println("  POST /v1/chat/completions")
                fmt.Println("  POST /v1/responses")
                fmt.Println("  POST /v1/messages")
                fmt.Println("press Ctrl+C to stop")
        } else {
                fmt.Println("proxy disabled (--no-proxy)")
        }
        if !*noRotate {
                fmt.Println("auto-rotation: enabled (use --no-rotate to disable)")
        } else {
                fmt.Println("auto-rotation: disabled")
        }
        if *autoReg {
                fmt.Printf("auto-registration: enabled (provider=%s, headed=%v)\n", *provider, *headed)
                fmt.Println("  when all accounts hit rate-limit, a fresh Grok account will be")
                fmt.Println("  created via temp email + Playwright and used to retry the request.")
        } else {
                fmt.Println("auto-registration: disabled (--no-auto-reg)")
        }

        // Show active account if any
        if acc, ok := a.ActiveAccount(); ok {
                fmt.Printf("active account: %s <%s>\n", acc.Label, acc.Email)
        } else {
                fmt.Println("no account configured — auto-registration will provision one on first request")
        }

        // Wait for signal
        ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
        defer cancel()
        <-ctx.Done()
        fmt.Println("\nshutting down…")
}

func runLogin(args []string) {
        a := mustApp()
        defer a.Close()
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
        defer cancel()

        fmt.Println("starting xAI device login…")
        start, err := a.StartDeviceLogin(ctx)
        if err != nil {
                fail("start device login: %v", err)
        }
        url := start.VerificationURL
        fmt.Println()
        fmt.Println("  1) Open this URL in a browser:")
        fmt.Printf("     %s\n", url)
        fmt.Println()
        fmt.Println("  2) When asked, enter the code:")
        fmt.Printf("     %s\n", start.UserCode)
        fmt.Println()
        fmt.Println("waiting for authorization (press Ctrl+C to cancel)…")

        acc, err := a.WaitDeviceLogin(ctx)
        if err != nil {
                fail("login: %v", err)
        }
        fmt.Printf("\nlogin OK — account: %s <%s>\n", acc.Label, acc.Email)
        fmt.Printf("saved to: %s\n", filepath.Join(a.DataDir(), "accounts"))
}

func runAccounts(args []string) {
        a := mustApp()
        defer a.Close()
        accs := a.ListAccounts()
        if len(accs) == 0 {
                fmt.Println("no accounts — run `grok-proxy-cli login`")
                return
        }
        active := a.ActiveAccountID()
        fmt.Printf("%-24s %-32s %-20s %s\n", "ID", "LABEL", "EMAIL", "ACTIVE")
        for _, acc := range accs {
                marker := ""
                if acc.ID == active {
                        marker = "*"
                }
                fmt.Printf("%-24s %-32s %-20s %s\n", truncate(acc.ID, 24), truncate(acc.Label, 32), truncate(acc.Email, 20), marker)
        }
}

func runUse(args []string) {
        if len(args) < 1 {
                fail("usage: grok-proxy-cli use <id-or-prefix>")
        }
        a := mustApp()
        defer a.Close()
        id, err := a.ResolveAccountID(args[0])
        if err != nil {
                fail("%v", err)
        }
        if err := a.SetActiveAccount(id); err != nil {
                fail("%v", err)
        }
        fmt.Printf("active account: %s\n", id)
}

func runLogout(args []string) {
        if len(args) < 1 {
                fail("usage: grok-proxy-cli logout <id-or-prefix>")
        }
        a := mustApp()
        defer a.Close()
        id, err := a.ResolveAccountID(args[0])
        if err != nil {
                fail("%v", err)
        }
        if err := a.RemoveAccount(id); err != nil {
                fail("%v", err)
        }
        fmt.Printf("removed account: %s\n", id)
}

func runModels(args []string) {
        a := mustApp()
        defer a.Close()
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        models, err := a.ListModels(ctx)
        if err != nil {
                fail("list models: %v", err)
        }
        fmt.Printf("%-32s %-32s %s\n", "ID", "NAME", "MODE")
        for _, m := range models {
                fmt.Printf("%-32s %-32s %s\n", truncate(m.ID, 32), truncate(m.Name, 32), m.APIMode)
        }
}

func runChat(args []string) {
        a := mustApp()
        defer a.Close()
        if _, ok := a.ActiveAccount(); !ok {
                fail("no account — run `grok-proxy-cli login`")
        }

        history := []app.ChatMessage{}
        reader := bufio.NewReader(os.Stdin)
        fmt.Println("grok-proxy-cli chat — type :q to quit, :clear to reset history")
        fmt.Println()
        for {
                fmt.Print("> ")
                line, err := reader.ReadString('\n')
                if err != nil {
                        fmt.Println()
                        return
                }
                text := strings.TrimSpace(line)
                if text == "" {
                        continue
                }
                if text == ":q" || text == ":quit" || text == ":exit" {
                        return
                }
                if text == ":clear" {
                        history = nil
                        fmt.Println("(history cleared)")
                        continue
                }
                history = append(history, app.ChatMessage{Role: "user", Content: text})
                fmt.Print("\n")
                ctx, cancel := context.WithCancel(context.Background())
                go func() {
                        ch := make(chan os.Signal, 1)
                        signal.Notify(ch, syscall.SIGINT)
                        <-ch
                        cancel()
                }()
                var sb strings.Builder
                err = a.StreamChat(ctx, history, func(ev app.ChatEvent) {
                        switch ev.Type {
                        case "thinking":
                                fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m", ev.Text)
                        case "content":
                                fmt.Print(ev.Text)
                                sb.WriteString(ev.Text)
                        case "usage":
                                fmt.Fprintf(os.Stderr, "\n\n[usage] prompt=%d completion=%d reasoning=%d total=%d\n",
                                        ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.ReasoningTokens, ev.Usage.TotalTokens)
                        case "error":
                                fmt.Fprintf(os.Stderr, "\n[error] %s\n", ev.Error)
                        }
                })
                cancel()
                if err != nil {
                        fmt.Fprintf(os.Stderr, "\n[stream error] %v\n", err)
                }
                fmt.Println("\n")
                history = append(history, app.ChatMessage{Role: "assistant", Content: sb.String()})
        }
}

func runAsk(args []string) {
        fs := flag.NewFlagSet("ask", flag.ExitOnError)
        effort := fs.String("effort", "high", "reasoning effort: low|medium|high")
        model := fs.String("model", "", "model id (default: settings.default_model)")
        noThink := fs.Bool("no-think", false, "disable reasoning")
        _ = fs.Parse(args)
        prompt := strings.Join(fs.Args(), " ")
        if prompt == "" {
                fail("usage: grok-proxy-cli ask \"your prompt here\"")
        }
        a := mustApp()
        defer a.Close()
        if _, ok := a.ActiveAccount(); !ok {
                fail("no account — run `grok-proxy-cli login`")
        }
        history := []app.ChatMessage{{Role: "user", Content: prompt}}
        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()
        go func() {
                ch := make(chan os.Signal, 1)
                signal.Notify(ch, syscall.SIGINT)
                <-ch
                cancel()
        }()
        opts := app.ChatOptions{Model: *model, Effort: *effort, NoThinking: *noThink}
        err := a.StreamChatWithOptions(ctx, history, opts, func(ev app.ChatEvent) {
                switch ev.Type {
                case "thinking":
                        fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m", ev.Text)
                case "content":
                        fmt.Print(ev.Text)
                case "usage":
                        fmt.Fprintf(os.Stderr, "\n[usage] prompt=%d completion=%d reasoning=%d total=%d\n",
                                ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.ReasoningTokens, ev.Usage.TotalTokens)
                case "error":
                        fmt.Fprintf(os.Stderr, "\n[error] %s\n", ev.Error)
                }
        })
        if err != nil {
                fmt.Fprintf(os.Stderr, "\n[stream error] %v\n", err)
                os.Exit(1)
        }
        fmt.Println()
}

func truncate(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[:n-1] + "…"
}

// runRotate implements `grok-proxy-cli rotate`.
//
//      grok-proxy-cli rotate                 # show rotation status
//      grok-proxy-cli rotate --next          # rotate to next available account now
//      grok-proxy-cli rotate --reset <id>    # clear limited-state for an account
//      grok-proxy-cli rotate --reset-all     # clear limited-state for all accounts
func runRotate(args []string) {
        fs := flag.NewFlagSet("rotate", flag.ExitOnError)
        next := fs.Bool("next", false, "rotate to the next available account now")
        resetID := fs.String("reset", "", "clear limited-state for the given account id (prefix supported)")
        resetAll := fs.Bool("reset-all", false, "clear limited-state for all accounts")
        _ = fs.Parse(args)

        a := mustApp()
        defer a.Close()
        r := a.Rotator()

        if *resetAll {
                for _, acc := range a.ListAccounts() {
                        r.MarkAvailable(acc.ID)
                }
                fmt.Println("cleared limited-state for all accounts")
                return
        }
        if *resetID != "" {
                id, err := a.ResolveAccountID(*resetID)
                if err != nil {
                        fail("%v", err)
                }
                r.MarkAvailable(id)
                fmt.Printf("cleared limited-state for account: %s\n", id)
                return
        }
        if *next {
                active := a.ActiveAccountID()
                nextID, ok := r.PickNextAvailable(active)
                if !ok {
                        fail("no other available account to rotate to")
                }
                if err := a.SetActiveAccount(nextID); err != nil {
                        fail("%v", err)
                }
                acc, _ := a.ActiveAccount()
                label := acc.Email
                if label == "" {
                        label = acc.Label
                }
                fmt.Printf("rotated to: %s <%s>\n", nextID, label)
                return
        }

        // default: print status
        accs := a.ListAccounts()
        if len(accs) == 0 {
                fmt.Println("no accounts — run `grok-proxy-cli login`")
                return
        }
        status := r.Status()
        active := a.ActiveAccountID()
        fmt.Printf("auto-rotation: %v\n", r.AutoRotate())
        fmt.Printf("%-24s %-28s %-20s %-10s %s\n", "ID", "LABEL", "EMAIL", "ACTIVE", "LIMITED")
        for _, acc := range accs {
                marker := ""
                if acc.ID == active {
                        marker = "*"
                }
                limited := "no"
                if st, ok := status[acc.ID]; ok && !st.LimitedUntil.IsZero() {
                        remaining := time.Until(st.LimitedUntil)
                        if remaining > 0 {
                                limited = fmt.Sprintf("%s (%s)", st.Reason, remaining.Round(time.Second))
                        }
                }
                fmt.Printf("%-24s %-28s %-20s %-10s %s\n",
                        truncate(acc.ID, 24), truncate(acc.Label, 28), truncate(acc.Email, 20), marker, limited)
        }
}

// runAutoReg implements `grok-proxy-cli autoreg`.
//
//      grok-proxy-cli autoreg                       create a new account automatically
//                                                   (default: headless Playwright + mail.tm)
//      grok-proxy-cli autoreg --provider tempmail.lol
//      grok-proxy-cli autoreg --no-browser          assisted mode (you complete the signup manually)
//      grok-proxy-cli autoreg --headed              show the Chromium window
//      grok-proxy-cli autoreg --keep-inbox          do not delete the temp inbox afterwards
func runAutoReg(args []string) {
        fs := flag.NewFlagSet("autoreg", flag.ExitOnError)
        provider := fs.String("provider", "mail.tm", "temp-mail provider: mail.tm | tempmail.lol")
        noBrowser := fs.Bool("no-browser", false, "assisted mode — print URL+email+code, you complete the signup in your own browser")
        headed := fs.Bool("headed", false, "show the Chromium window during the automated signup (debug)")
        keepInbox := fs.Bool("keep-inbox", false, "do not delete the temp inbox after the flow")
        emailWait := fs.Duration("email-wait", 5*time.Minute, "how long to wait for the verification email")
        signupTimeout := fs.Duration("signup-timeout", 10*time.Minute, "overall timeout for the auto-registration flow")
        _ = fs.Parse(args)

        a := mustApp()
        defer a.Close()

        fmt.Printf("auto-registro: provider=%s, browser=%v, headed=%v\n", *provider, !*noBrowser, *headed)
        fmt.Printf("  email-wait=%s  signup-timeout=%s  keep-inbox=%v\n", *emailWait, *signupTimeout, *keepInbox)
        fmt.Println()

        ctx, cancel := context.WithTimeout(context.Background(), *signupTimeout)
        defer cancel()

        logger := func(format string, args ...any) {
                fmt.Fprintf(os.Stderr, "[autoreg] "+format+"\n", args...)
        }

        acc, inbox, err := a.RegisterNewAccount(ctx, app.AutoRegOptions{
                ProviderName:     *provider,
                EmailWaitTimeout: *emailWait,
                SignupTimeout:    *signupTimeout,
                CleanupInbox:     !*keepInbox,
                AutomatedSignup:  !*noBrowser,
                Headed:           *headed,
                Logger:           logger,
        })
        if err != nil {
                fail("auto-registro falhou: %v", err)
        }
        fmt.Println()
        fmt.Println("✓ conta criada e ativada com sucesso!")
        fmt.Printf("  ID:     %s\n", acc.ID)
        fmt.Printf("  email:  %s\n", acc.Email)
        fmt.Printf("  label:  %s\n", acc.Label)
        if inbox != nil {
                fmt.Printf("  inbox:  %s (%s)\n", inbox.Address, inbox.Provider)
        }
        fmt.Printf("  salvo em: %s\n", filepath.Join(a.DataDir(), "accounts"))
}
