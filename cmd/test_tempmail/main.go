// Command test_tempmail validates that the temp-mail providers are
// reachable and able to provision an inbox. Run with:
//
//	go run ./cmd/test_tempmail
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"grok-desktop/internal/tempmail"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("==> Testando provedor mail.tm…")
	p := tempmail.NewMailTmProvider()
	inbox, err := p.CreateInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FALHA mail.tm: %v\n", err)
	} else {
		fmt.Printf("OK! Inbox criado:\n")
		fmt.Printf("  Address:   %s\n", inbox.Address)
		fmt.Printf("  Provider:  %s\n", inbox.Provider)
		fmt.Printf("  AccountID: %s\n", inbox.AccountID)
		fmt.Printf("  Token:     %s...(%d chars)\n", inbox.Token[:30], len(inbox.Token))
	}

	fmt.Println()
	fmt.Println("==> Testando provedor tempmail.lol (fallback)…")
	p2 := tempmail.NewTempmailLolProvider()
	inbox2, err := p2.CreateInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN tempmail.lol: %v\n", err)
	} else {
		fmt.Printf("OK! Inbox criado:\n")
		fmt.Printf("  Address:  %s\n", inbox2.Address)
		fmt.Printf("  Provider: %s\n", inbox2.Provider)
		fmt.Printf("  Token:    %s...(%d chars)\n", inbox2.Token[:30], len(inbox2.Token))
	}
}
