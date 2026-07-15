// Command test_code validates ExtractVerificationCode against real x.ai
// email subjects/bodies that we observed in production.
package main

import (
	"fmt"
	"os"

	"grok-desktop/internal/tempmail"
)

func main() {
	cases := []struct {
		name string
		msg  tempmail.Message
		want string
	}{
		{
			name: "x.ai subject with code",
			msg: tempmail.Message{
				Subject: "X6B-09B xAI confirmation code",
				From:    "noreply@x.ai",
			},
			want: "X6B-09B",
		},
		{
			name: "x.ai subject 'Your code is'",
			msg: tempmail.Message{
				Subject: "Your xAI verification code is F2K-7P1",
			},
			want: "F2K-7P1",
		},
		{
			name: "code only in body (lowercase)",
			msg: tempmail.Message{
				Subject: "Verify your email",
				Text:    "Your code is abc-123 please enter it",
			},
			want: "ABC-123",
		},
		{
			name: "no code anywhere",
			msg: tempmail.Message{
				Subject: "Welcome",
				Text:    "Thanks for signing up",
			},
			want: "",
		},
		{
			name: "subject has logo URL but no code",
			msg: tempmail.Message{
				Subject: "Verify your email address",
				HTML:    `<a href="https://data.x.ai/email-attachments/spacexai-logo-light.png">`,
			},
			want: "",
		},
	}

	pass := 0
	for _, c := range cases {
		got := tempmail.ExtractVerificationCode(&c.msg)
		status := "✓"
		if got != c.want {
			status = "✗"
		} else {
			pass++
		}
		fmt.Printf("%s  %s — got=%q want=%q\n", status, c.name, got, c.want)
	}
	fmt.Printf("\n%d/%d testes passaram\n", pass, len(cases))
	if pass != len(cases) {
		os.Exit(1)
	}
}
