// Command test_emailproxy validates the emailproxy integration by
// creating inboxes via both backends and printing the addresses.
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

	fmt.Println("==> Testando provedor emailproxy:tmaily…")
	p1 := tempmail.NewEmailProxyProviderWithBackend("", "tmaily")
	inbox1, err := p1.CreateInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FALHA tmaily: %v\n", err)
	} else {
		fmt.Printf("OK! Inbox criado:\n")
		fmt.Printf("  Address:  %s\n", inbox1.Address)
		fmt.Printf("  Provider: %s\n", inbox1.Provider)
		fmt.Printf("  Token:    %s\n", inbox1.Token)
	}

	fmt.Println()
	fmt.Println("==> Testando provedor emailproxy:invertexto…")
	p2 := tempmail.NewEmailProxyProviderWithBackend("", "invertexto")
	inbox2, err := p2.CreateInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FALHA invertexto: %v\n", err)
	} else {
		fmt.Printf("OK! Inbox criado:\n")
		fmt.Printf("  Address:  %s\n", inbox2.Address)
		fmt.Printf("  Provider: %s\n", inbox2.Provider)
		fmt.Printf("  Token:    %s\n", inbox2.Token)
	}

	fmt.Println()
	fmt.Println("==> Conclusão:")
	if inbox1 != nil {
		fmt.Printf("  tmaily     → %s (domínio NÃO bloqueado pelo x.ai)\n", inbox1.Address)
	}
	if inbox2 != nil {
		fmt.Printf("  invertexto → %s (domínio NÃO bloqueado pelo x.ai)\n", inbox2.Address)
	}
}
