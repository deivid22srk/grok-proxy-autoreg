// Command test_oauth starts an x.ai device-code flow and prints the
// verification URL + user code. Validates that the x.ai OAuth endpoint
// is reachable and responding with a device code.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"grok-desktop/internal/oauth"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("==> Iniciando device-code flow no x.ai…")
	c := oauth.New()
	start, err := c.StartDevice(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FALHA StartDevice: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK! Device code obtido:")
	fmt.Printf("  DeviceCode:      %s…\n", start.DeviceCode[:30])
	fmt.Printf("  UserCode:        %s\n", start.UserCode)
	fmt.Printf("  VerificationURI: %s\n", start.VerificationURI)
	fmt.Printf("  VerifyComplete:  %s\n", start.VerificationURIComplete)
	fmt.Printf("  ExpiresIn:       %ds\n", start.ExpiresIn)
	fmt.Printf("  Interval:        %ds\n", start.Interval)
}
