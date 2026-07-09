// Self-test: store AppData layout + live chat request (no GUI).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"grok-desktop/internal/oauth"
	"grok-desktop/internal/store"
	"grok-desktop/internal/upstream"
)

func main() {
	fmt.Println("=== GrokDesktop selftest ===")
	st, err := store.Open("")
	if err != nil {
		fail("store open: %v", err)
	}
	root := st.Root()
	fmt.Println("data_dir:", root)
	fmt.Println("settings:", filepath.Join(root, "settings.json"))
	fmt.Println("usage:   ", filepath.Join(root, "usage.json"))
	fmt.Println("accounts:", filepath.Join(root, "accounts"))

	// list files
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Printf("  file %s (%d bytes)\n", rel, info.Size())
		return nil
	})

	acc, ok := st.ActiveAccount()
	if !ok || acc == nil {
		fail("no account after migration — add one in the app UI first")
	}
	fmt.Printf("active account: %s <%s> exp=%s expired=%v\n", acc.Label, acc.Email, acc.ExpiresAt.Format(time.RFC3339), acc.Expired())

	// refresh if needed
	oa := oauth.New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if acc.ExpiresSoon(5*time.Minute) && acc.RefreshToken != "" {
		fmt.Println("refreshing token…")
		tok, err := oa.Refresh(ctx, acc.RefreshToken, acc.ClientID, acc.Issuer)
		if err != nil {
			fail("refresh: %v", err)
		}
		acc.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			acc.RefreshToken = tok.RefreshToken
		}
		acc.ExpiresAt = time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
		if err := st.UpsertAccount(*acc); err != nil {
			fail("save account: %v", err)
		}
		fmt.Println("refresh OK, new exp", acc.ExpiresAt.Format(time.RFC3339))
	}

	settings := st.Settings()
	up := upstream.New()

	fmt.Println("listing models…")
	models, err := up.ListModels(ctx, acc.AccessToken, settings)
	if err != nil {
		fail("models: %v", err)
	}
	ids := make([]string, 0, 4)
	for i, m := range models {
		if i >= 4 {
			break
		}
		ids = append(ids, m.ID)
	}
	fmt.Println("models OK:", strings.Join(ids, ", "), "… total", len(models))

	fmt.Println("streaming chat…")
	var content, thinking strings.Builder
	var usage *upstream.Usage
	var respID string
	err = up.StreamChat(ctx, acc.AccessToken, settings, acc.Label, acc.Email, upstream.ChatRequest{
		Model:           "grok-4.5",
		Messages:        []upstream.ChatMessage{{Role: "user", Content: "Reply with exactly: appdata-ok"}},
		Stream:          true,
		ReasoningEffort: "low",
		APIMode:         "chat",
	}, func(ev upstream.StreamEvent) {
		switch ev.Type {
		case "thinking":
			thinking.WriteString(ev.Text)
		case "content":
			content.WriteString(ev.Text)
		case "usage":
			usage = ev.Usage
		case "done":
			respID = ev.ID
		case "error":
			fmt.Println("stream error event:", ev.Error)
		}
	})
	if err != nil {
		fail("chat stream: %v", err)
	}
	fmt.Println("response id:", respID)
	fmt.Println("content:    ", strings.TrimSpace(content.String()))
	fmt.Println("thinking len:", thinking.Len())
	if usage != nil {
		fmt.Printf("usage: prompt=%d completion=%d reasoning=%d total=%d\n",
			usage.PromptTokens, usage.CompletionTokens, usage.ReasoningTokens, usage.TotalTokens)
		_ = st.AddUsage(acc.ID, usage.PromptTokens, usage.CompletionTokens, usage.ReasoningTokens)
	}

	// verify account file exists under AppData
	accFile := filepath.Join(root, "accounts", acc.ID+".json")
	if _, err := os.Stat(accFile); err != nil {
		// try sanitized walk
		found := false
		_ = filepath.Walk(filepath.Join(root, "accounts"), func(p string, info os.FileInfo, e error) error {
			if e == nil && !info.IsDir() && strings.HasSuffix(p, ".json") {
				found = true
				fmt.Println("account file:", p)
			}
			return nil
		})
		if !found {
			fail("no account file under AppData/accounts")
		}
	} else {
		fmt.Println("account file:", accFile)
	}

	if !strings.Contains(strings.ToLower(content.String()), "appdata-ok") {
		fmt.Println("WARN: content did not contain exact phrase (model may have paraphrased)")
	}
	fmt.Println("=== SELFTEST OK ===")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
