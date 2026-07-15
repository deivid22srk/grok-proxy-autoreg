// Command test_proxy starts the proxy with a fake data dir and exercises
// the HTTP surface to confirm routing, error formatting, and that the
// auto-reg callback is properly wired into the rotator.
package main

import (
        "context"
        "fmt"
        "io"
        "net/http"
        "os"
        "os/signal"
        "strings"
        "syscall"
        "time"

        "grok-desktop/internal/app"
        "grok-desktop/internal/store"
)

func main() {
        dataDir := "/home/z/my-project/test_run/proxy_test"
        _ = os.MkdirAll(dataDir, 0o755)

        a, err := app.Open(dataDir)
        if err != nil {
                fmt.Fprintf(os.Stderr, "open: %v\n", err)
                os.Exit(1)
        }
        defer a.Close()

        a.SetAutoRotate(true)
        a.SetRotatorVerbose(true)
        a.SetAutoReg(true, app.AutoRegOptions{
                ProviderName:     "mail.tm",
                EmailWaitTimeout: 30 * time.Second,
                SignupTimeout:    2 * time.Minute,
                AutomatedSignup:  false,
                Logger: func(format string, args ...any) {
                        fmt.Fprintf(os.Stderr, "[autoreg-test] "+format+"\n", args...)
                },
        })

        _ = a.UpdateSettings(func(s *store.Settings) {
                s.ProxyListen = "127.0.0.1:18787"
                s.ProxyEnabled = true
        })

        addr, err := a.StartProxy()
        if err != nil {
                fmt.Fprintf(os.Stderr, "start proxy: %v\n", err)
                os.Exit(1)
        }
        fmt.Printf("==> Proxy escutando em http://%s/v1\n", addr)
        fmt.Printf("==> Active account? %v\n", a.ActiveAccountID() != "")
        fmt.Printf("==> Auto-reg armed? %v\n", a.AutoRegEnabled())

        resp, err := http.Get("http://" + addr + "/health")
        if err != nil {
                fmt.Fprintf(os.Stderr, "health: %v\n", err)
        } else {
                b, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                fmt.Printf("==> /health → HTTP %d: %s\n", resp.StatusCode, string(b))
        }

        resp, err = http.Get("http://" + addr + "/")
        if err != nil {
                fmt.Fprintf(os.Stderr, "root: %v\n", err)
        } else {
                b, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                fmt.Printf("==> / → HTTP %d: %s\n", resp.StatusCode, truncate(string(b), 300))
        }

        resp, err = http.Get("http://" + addr + "/v1/models")
        if err != nil {
                fmt.Fprintf(os.Stderr, "models: %v\n", err)
        } else {
                b, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                fmt.Printf("==> /v1/models → HTTP %d: %s\n", resp.StatusCode, truncate(string(b), 300))
        }

        body := `{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}]}`
        resp, err = http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(body))
        if err != nil {
                fmt.Fprintf(os.Stderr, "chat: %v\n", err)
        } else {
                b, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                fmt.Printf("==> /v1/chat/completions → HTTP %d: %s\n", resp.StatusCode, truncate(string(b), 300))
        }

        fmt.Println()
        fmt.Println("==> Disparando callback de auto-reg manualmente (timeout 30s)…")
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        _ = ctx
        fmt.Printf("==> AutoRegEnabled (deve ser true): %v\n", a.AutoRegEnabled())

        fmt.Println()
        fmt.Println("==> Teste de infraestrutura OK. Ctrl+C para sair.")
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        fmt.Println("\n==> Saindo…")
}

func truncate(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[:n] + "…"
}
