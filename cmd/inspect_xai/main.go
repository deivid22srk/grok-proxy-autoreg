// Command inspect_xai launches a headless Chromium via Playwright,
// navigates to the x.ai device-code page (without a user_code so it
// shows the generic form), waits for the page to settle, then dumps:
//
//   - the current URL
//   - the page title
//   - all visible buttons/links (text + selector)
//   - all visible input fields (type + name + id + placeholder)
//   - the first 4000 chars of <body> innerText
//   - the full HTML saved to ./inspect_xai_dump.html
//
// Usage:
//
//      go run ./cmd/inspect_xai [url]
package main

import (
        "context"
        "fmt"
        "os"
        "time"

        playwright "github.com/mxschmitt/playwright-go"
)

func main() {
        url := "https://accounts.x.ai/oauth2/device"
        if len(os.Args) > 1 {
                url = os.Args[1]
        }

        fmt.Println("==> Instalando Playwright (se necessário)…")
        if err := playwright.Install(); err != nil {
                fmt.Fprintf(os.Stderr, "install: %v\n", err)
                os.Exit(1)
        }

        fmt.Println("==> Lançando Chromium headless…")
        pw, err := playwright.Run()
        if err != nil {
                fmt.Fprintf(os.Stderr, "run: %v\n", err)
                os.Exit(1)
        }
        defer pw.Stop()

        browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
                Headless: playwright.Bool(true),
                Args: []string{
                        "--no-sandbox",
                        "--disable-blink-features=AutomationControlled",
                        "--disable-dev-shm-usage",
                },
        })
        if err != nil {
                fmt.Fprintf(os.Stderr, "launch: %v\n", err)
                os.Exit(1)
        }
        defer browser.Close()

        ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
                UserAgent: playwright.String("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"),
                Viewport: &playwright.Size{
                        Width:  1280,
                        Height: 800,
                },
                Locale:            playwright.String("en-US"),
                IgnoreHttpsErrors: playwright.Bool(true),
        })
        if err != nil {
                fmt.Fprintf(os.Stderr, "context: %v\n", err)
                os.Exit(1)
        }
        defer ctx.Close()

        page, err := ctx.NewPage()
        if err != nil {
                fmt.Fprintf(os.Stderr, "page: %v\n", err)
                os.Exit(1)
        }
        defer page.Close()

        // Anti-bot evasion.
        _ = page.AddInitScript(playwright.Script{
                Content: playwright.String(`Object.defineProperty(navigator, 'webdriver', {get: () => undefined});`),
        })

        c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
        defer cancel()
        _ = c

        fmt.Printf("==> Navegando para %s\n", url)
        if _, err := page.Goto(url, playwright.PageGotoOptions{
                WaitUntil: playwright.WaitUntilStateNetworkidle,
                Timeout:   playwright.Float(60000),
        }); err != nil {
                fmt.Fprintf(os.Stderr, "goto: %v\n", err)
                // Continua mesmo com erro para fazer dump do que tiver.
        }

        // Wait a bit for any SPA hydration / Cloudflare challenge.
        time.Sleep(8 * time.Second)

        fmt.Printf("\n==> URL atual: %s\n", page.URL())
        title, _ := page.Title()
        fmt.Printf("==> Title: %s\n\n", title)

        // Dump all input fields.
        fmt.Println("==> INPUTS:")
        inputs, err := page.Locator("input").All()
        if err != nil {
                fmt.Fprintf(os.Stderr, "inputs: %v\n", err)
        }
        for i, el := range inputs {
                typ, _ := el.GetAttribute("type")
                name, _ := el.GetAttribute("name")
                id, _ := el.GetAttribute("id")
                ph, _ := el.GetAttribute("placeholder")
                visible, _ := el.IsVisible()
                fmt.Printf("  [%d] type=%q name=%q id=%q placeholder=%q visible=%v\n", i, strOr(typ), strOr(name), strOr(id), strOr(ph), visible)
        }

        // Dump all buttons.
        fmt.Println("\n==> BUTTONS:")
        buttons, err := page.Locator("button").All()
        if err != nil {
                fmt.Fprintf(os.Stderr, "buttons: %v\n", err)
        }
        for i, el := range buttons {
                text, _ := el.InnerText()
                typ, _ := el.GetAttribute("type")
                visible, _ := el.IsVisible()
                fmt.Printf("  [%d] type=%q text=%q visible=%v\n", i, strOr(typ), truncate(text, 60), visible)
        }

        // Dump all links.
        fmt.Println("\n==> LINKS (a):")
        links, err := page.Locator("a").All()
        if err != nil {
                fmt.Fprintf(os.Stderr, "links: %v\n", err)
        }
        for i, el := range links {
                text, _ := el.InnerText()
                href, _ := el.GetAttribute("href")
                visible, _ := el.IsVisible()
                if text == "" && href == "" {
                        continue
                }
                fmt.Printf("  [%d] text=%q href=%q visible=%v\n", i, truncate(text, 60), strOr(href), visible)
                if i > 30 {
                        break
                }
        }

        // Dump body text.
        fmt.Println("\n==> BODY INNER TEXT (first 4000 chars):")
        bodyText, _ := page.InnerText("body")
        if len(bodyText) > 4000 {
                bodyText = bodyText[:4000]
        }
        fmt.Println(bodyText)

        // Save HTML.
        html, _ := page.Content()
        if html != "" {
                _ = os.WriteFile("/home/z/my-project/test_run/inspect_xai_dump.html", []byte(html), 0o644)
                fmt.Println("\n==> HTML salvo em /home/z/my-project/test_run/inspect_xai_dump.html")
        }
}

func strOr(s string) string {
        if s == "" {
                return "-"
        }
        return s
}

func truncate(s string, n int) string {
        // trim whitespace
        for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r') {
                s = s[1:]
        }
        for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
                s = s[:len(s)-1]
        }
        if len(s) <= n {
                return s
        }
        return s[:n-1] + "…"
}
