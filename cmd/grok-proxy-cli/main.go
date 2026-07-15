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
        case "autoreg-batch":
                runAutoRegBatch(args)
        default:
                fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
                printHelp()
                os.Exit(2)
        }
}

func printHelp() {
        fmt.Printf(`grok-proxy-cli %s — terminal-only Grok proxy with auto-rotation
and assisted account registration via temporary email.

Usage:
  grok-proxy-cli                       start the local OpenAI proxy (default = serve)
  grok-proxy-cli serve                 same as above; flags: --listen, --api-key, --no-proxy,
                                       --no-rotate, --rotate-verbose, --auto-reg,
                                       --provider <mail.tm|tempmail.lol>, --keep-inbox,
                                       --email-wait, --signup-timeout
  grok-proxy-cli login                 sign in with xAI device-code OAuth (manual)
  grok-proxy-cli autoreg               provisiona um email temporário, inicia o device-code
                                       flow do x.ai e monitra o inbox. Você abre a URL no
                                       seu navegador, cria a conta com o email temporário,
                                       e o programa detecta o email de verificação e mostra
                                       o código. Flags: --provider, --keep-inbox, --email-wait
  grok-proxy-cli autoreg-batch N       cria N contas em sequência (uma por vez). Após cada
                                       conta ser criada e salva, inicia a próxima automaticamente.
                                       Flags: --provider, --keep-inbox, --email-wait,
                                       --signup-timeout, --pause (pausa entre contas, default 5s)
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

Auto-registration (assisted):
  With --auto-reg (or via 'autoreg' command), the program provisions a
  temp email on mail.tm (or tempmail.lol via --provider), starts the x.ai
  device-code flow, and prints:
    1. The verification URL (open in your browser)
    2. The temp email to use in the signup form
    3. The device code (if asked)
  It then polls the temp inbox. When the verification email from x.ai
  arrives, it extracts the verification link and:
    - Tries to confirm via HTTP (best-effort)
    - Prints the link so you can click it manually if needed
  Finally, polls the OAuth endpoint until access+refresh tokens arrive,
  saves the new account, and (if triggered from 'serve') retries the
  original request.
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
        autoReg := fs.Bool("auto-reg", false, "auto-register a fresh account (assisted) when all accounts are limited")
        provider := fs.String("provider", "mail.tm", "temp-mail provider: mail.tm | tempmail.lol")
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

        // Wire up auto-registration (assisted mode — user completes signup in
        // their own browser, the program monitors the inbox and prints the
        // verification link).
        a.SetAutoReg(*autoReg, app.AutoRegOptions{
                ProviderName:     *provider,
                EmailWaitTimeout: *emailWait,
                SignupTimeout:    *signupTimeout,
                CleanupInbox:     !*keepInbox,
                AutomatedSignup:  false, // always assisted now (no Playwright)
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
                fmt.Printf("auto-registration: enabled (provider=%s)\n", *provider)
                fmt.Println("  when all accounts hit rate-limit, the program will print a")
                fmt.Println("  URL + temp email — you complete the signup in your browser")
                fmt.Println("  and it auto-detects the verification email.")
        } else {
                fmt.Println("auto-registration: disabled (use --auto-reg to enable)")
        }

        // Show active account if any
        if acc, ok := a.ActiveAccount(); ok {
                fmt.Printf("active account: %s <%s>\n", acc.Label, acc.Email)
        } else {
                fmt.Println("no account configured — run `grok-proxy-cli autoreg` first,")
                fmt.Println("or run serve with --auto-reg to provision on first 429.")
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
//      grok-proxy-cli autoreg                       create a new account (assisted)
//      grok-proxy-cli autoreg --provider tempmail.lol
//      grok-proxy-cli autoreg --keep-inbox          do not delete the temp inbox afterwards
//      grok-proxy-cli autoreg --email-wait 10m      wait up to 10 min for the verification email
func runAutoReg(args []string) {
        fs := flag.NewFlagSet("autoreg", flag.ExitOnError)
        provider := fs.String("provider", "mail.tm", "temp-mail provider: mail.tm | tempmail.lol")
        keepInbox := fs.Bool("keep-inbox", false, "do not delete the temp inbox after the flow")
        emailWait := fs.Duration("email-wait", 10*time.Minute, "how long to wait for the verification email")
        signupTimeout := fs.Duration("signup-timeout", 15*time.Minute, "overall timeout for the auto-registration flow")
        _ = fs.Parse(args)

        a := mustApp()
        defer a.Close()

        fmt.Printf("auto-registro (modo assistido): provider=%s\n", *provider)
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
                AutomatedSignup:  false, // always assisted (no Playwright)
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

// runAutoRegBatch implements `grok-proxy-cli autoreg-batch N`.
//
//      grok-proxy-cli autoreg-batch 2                cria 2 contas em sequência
//      grok-proxy-cli autoreg-batch 5 --pause 10s    cria 5 contas, 10s entre cada
//      grok-proxy-cli autoreg-batch 3 --provider emailproxy:invertexto
//
// O fluxo de cada conta é idêntico ao `autoreg`:
//  1. Provisiona email temporário
//  2. Inicia device-code flow do x.ai
//  3. Imprime URL + email + código do dispositivo
//  4. Aguarda email de verificação
//  5. Extrai código de verificação do assunto e exibe em banner
//  6. Faz poll OAuth até receber tokens
//  7. Salva conta no store
//
// Após cada conta ser salva, aguarda --pause segundos e inicia a próxima.
// Se uma conta falhar, pergunta ao usuário se deseja continuar com a próxima
// ou abortar o batch.
func runAutoRegBatch(args []string) {
        fs := flag.NewFlagSet("autoreg-batch", flag.ExitOnError)
        provider := fs.String("provider", "emailproxy:tmaily", "temp-mail provider")
        keepInbox := fs.Bool("keep-inbox", false, "do not delete the temp inbox after the flow")
        emailWait := fs.Duration("email-wait", 10*time.Minute, "how long to wait for the verification email (per account)")
        signupTimeout := fs.Duration("signup-timeout", 15*time.Minute, "overall timeout per account")
        pause := fs.Duration("pause", 5*time.Second, "pause between accounts")
        _ = fs.Parse(args)

        // O número de contas é o primeiro argumento posicional.
        posArgs := fs.Args()
        if len(posArgs) < 1 {
                fail("usage: grok-proxy-cli autoreg-batch N [--provider ...] [--pause 5s]")
        }
        var n int
        if _, err := fmt.Sscanf(posArgs[0], "%d", &n); err != nil || n < 1 {
                fail("número de contas inválido: %q (use um inteiro >= 1)", posArgs[0])
        }

        a := mustApp()
        defer a.Close()

        fmt.Printf("=== AUTOREG-BATCH: criando %d conta(s) em sequência ===\n", n)
        fmt.Printf("  provider=%s  email-wait=%s  signup-timeout=%s  pause=%s  keep-inbox=%v\n",
                *provider, *emailWait, *signupTimeout, *pause, *keepInbox)
        fmt.Println()

        logger := func(format string, args ...any) {
                fmt.Fprintf(os.Stderr, "[autoreg] "+format+"\n", args...)
        }

        type result struct {
                Index    int
                Email    string
                ID       string
                Label    string
                Error    error
                Duration time.Duration
        }
        results := make([]result, 0, n)

        for i := 1; i <= n; i++ {
                fmt.Printf("########## CONTA %d de %d ##########\n", i, n)
                start := time.Now()
                ctx, cancel := context.WithTimeout(context.Background(), *signupTimeout)
                acc, inbox, err := a.RegisterNewAccount(ctx, app.AutoRegOptions{
                        ProviderName:     *provider,
                        EmailWaitTimeout: *emailWait,
                        SignupTimeout:    *signupTimeout,
                        CleanupInbox:     !*keepInbox,
                        AutomatedSignup:  false,
                        Logger:           logger,
                })
                cancel()
                duration := time.Since(start).Round(time.Second)

                if err != nil {
                        fmt.Printf("\n✗ CONTA %d FALHOU após %s: %v\n", i, duration, err)
                        results = append(results, result{Index: i, Error: err, Duration: duration})
                        // Pergunta se quer continuar com a próxima
                        if i < n {
                                fmt.Println()
                                fmt.Print("Deseja continuar com a próxima conta? [s/N]: ")
                                reader := bufio.NewReader(os.Stdin)
                                ans, _ := reader.ReadString('\n')
                                ans = strings.TrimSpace(strings.ToLower(ans))
                                if ans != "s" && ans != "sim" && ans != "y" && ans != "yes" {
                                        fmt.Println("Batch abortado pelo usuário.")
                                        break
                                }
                        }
                } else {
                        fmt.Println()
                        fmt.Printf("✓ CONTA %d CRIADA E ATIVADA em %s!\n", i, duration)
                        fmt.Printf("  ID:     %s\n", acc.ID)
                        fmt.Printf("  email:  %s\n", acc.Email)
                        fmt.Printf("  label:  %s\n", acc.Label)
                        if inbox != nil {
                                fmt.Printf("  inbox:  %s (%s)\n", inbox.Address, inbox.Provider)
                        }
                        fmt.Printf("  salvo em: %s\n", filepath.Join(a.DataDir(), "accounts"))
                        results = append(results, result{
                                Index: i, Email: acc.Email, ID: acc.ID, Label: acc.Label,
                                Duration: duration,
                        })
                }

                // Pausa entre contas (exceto após a última)
                if i < n {
                        fmt.Printf("\nAguardando %s antes de iniciar a próxima conta…\n", *pause)
                        time.Sleep(*pause)
                        fmt.Println()
                }
        }

        // Resumo final
        fmt.Println()
        fmt.Println("============== RESUMO DO BATCH ==============")
        success := 0
        for _, r := range results {
                if r.Error != nil {
                        fmt.Printf("  Conta %d: ✗ FALHOU (%v) — %s\n", r.Index, r.Error, r.Duration)
                } else {
                        fmt.Printf("  Conta %d: ✓ OK — %s <%s> — %s\n", r.Index, r.ID, r.Email, r.Duration)
                        success++
                }
        }
        fmt.Printf("\nTotal: %d/%d contas criadas com sucesso.\n", success, n)
        fmt.Println("==============================================")
        if success > 0 {
                // Lista contas atuais
                fmt.Println()
                fmt.Println("Contas disponíveis no store:")
                runAccounts(nil)
        }
}
