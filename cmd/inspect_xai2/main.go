// Command inspect_xai2 is a more aggressive variant that waits longer
// for the Cloudflare challenge to resolve, uses a more realistic browser
// fingerprint, and dumps the resulting page.
package main

import (
        "context"
        "fmt"
        "os"
        "strings"
        "time"

        playwright "github.com/mxschmitt/playwright-go"
)

func main() {
        url := "https://accounts.x.ai/oauth2/device"
        if len(os.Args) > 1 {
                url = os.Args[1]
        }

        fmt.Println("==> Lançando Chromium headless (fingerprint reforçado)…")
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
                        "--disable-features=IsolateOrigins,site-per-process",
                        "--disable-site-isolation-trials",
                },
        })
        if err != nil {
                fmt.Fprintf(os.Stderr, "launch: %v\n", err)
                os.Exit(1)
        }
        defer browser.Close()

        ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
                UserAgent: playwright.String("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.6723.116 Safari/537.36"),
                Viewport: &playwright.Size{
                        Width:  1920,
                        Height: 1080,
                },
                Locale:            playwright.String("en-US"),
                TimezoneId:        playwright.String("America/Sao_Paulo"),
                IgnoreHttpsErrors: playwright.Bool(true),
                ExtraHttpHeaders: map[string]string{
                        "Accept-Language":         "en-US,en;q=0.9,pt-BR;q=0.8",
                        "Accept":                  "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
                        "Sec-Ch-Ua":               `"Chromium";v="130", "Not?A_Brand";v="99", "Google Chrome";v="130"`,
                        "Sec-Ch-Ua-Mobile":        "?0",
                        "Sec-Ch-Ua-Platform":      `"Linux"`,
                        "Sec-Fetch-Dest":          "document",
                        "Sec-Fetch-Mode":          "navigate",
                        "Sec-Fetch-Site":          "none",
                        "Sec-Fetch-User":          "?1",
                        "Upgrade-Insecure-Requests": "1",
                },
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

        // Aggressive anti-bot evasion.
        evasion := `
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en', 'pt-BR']});
Object.defineProperty(navigator, 'plugins', {get: () => [
        {name:'Chrome PDF Plugin',filename:'internal-pdf-viewer',description:'Portable Document Format'},
        {name:'Chrome PDF Viewer',filename:'mhjfbmdgcfjbbpaeojofohoefgiehjai',description:''},
        {name:'Native Client',filename:'internal-nacl-plugin',description:''}
]});
window.chrome = window.chrome || {runtime:{}};
Object.defineProperty(navigator, 'permissions', {get: () => ({query: () => Promise.resolve({state:'granted'})})});
`
        _ = page.AddInitScript(playwright.Script{Content: playwright.String(evasion)})

        c, cancel := context.WithTimeout(context.Background(), 120*time.Second)
        defer cancel()
        _ = c

        fmt.Printf("==> Navegando para %s\n", url)
        if _, err := page.Goto(url, playwright.PageGotoOptions{
                WaitUntil: playwright.WaitUntilStateDomcontentloaded,
                Timeout:   playwright.Float(60000),
        }); err != nil {
                fmt.Fprintf(os.Stderr, "goto: %v\n", err)
        }

        // Wait for Cloudflare challenge to resolve. Try up to 40 seconds.
        fmt.Println("==> Aguardando Cloudflare resolver (até 40s)…")
        for i := 0; i < 8; i++ {
                time.Sleep(5 * time.Second)
                title, _ := page.Title()
                bodyText, _ := page.InnerText("body")
                curURL := page.URL()
                fmt.Printf("  [%d] title=%q bodylen=%d url=%s\n", i, truncate(title, 60), len(bodyText), curURL)
                if title != "" && !strings.Contains(bodyText, "Blocked") && !strings.Contains(bodyText, "Just a moment") {
                        fmt.Println("  ✓ Cloudflare parece ter resolvido!")
                        break
                }
        }

        fmt.Printf("\n==> URL final: %s\n", page.URL())
        title, _ := page.Title()
        fmt.Printf("==> Title: %s\n\n", title)

        fmt.Println("==> INPUTS:")
        inputs, _ := page.Locator("input").All()
        for i, el := range inputs {
                typ, _ := el.GetAttribute("type")
                name, _ := el.GetAttribute("name")
                id, _ := el.GetAttribute("id")
                ph, _ := el.GetAttribute("placeholder")
                visible, _ := el.IsVisible()
                fmt.Printf("  [%d] type=%q name=%q id=%q placeholder=%q visible=%v\n", i, strOr(typ), strOr(name), strOr(id), strOr(ph), visible)
        }

        fmt.Println("\n==> BUTTONS:")
        buttons, _ := page.Locator("button").All()
        for i, el := range buttons {
                text, _ := el.InnerText()
                typ, _ := el.GetAttribute("type")
                visible, _ := el.IsVisible()
                fmt.Printf("  [%d] type=%q text=%q visible=%v\n", i, strOr(typ), truncate(text, 60), visible)
        }

        fmt.Println("\n==> LINKS (a):")
        links, _ := page.Locator("a").All()
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

        fmt.Println("\n==> BODY INNER TEXT (first 6000 chars):")
        bodyText, _ := page.InnerText("body")
        if len(bodyText) > 6000 {
                bodyText = bodyText[:6000]
        }
        fmt.Println(bodyText)

        html, _ := page.Content()
        if html != "" {
                _ = os.WriteFile("/home/z/my-project/test_run/inspect_xai2_dump.html", []byte(html), 0o644)
                fmt.Println("\n==> HTML salvo em /home/z/my-project/test_run/inspect_xai2_dump.html")
        }
}

func strOr(s string) string {
        if s == "" {
                return "-"
        }
        return s
}

func truncate(s string, n int) string {
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
