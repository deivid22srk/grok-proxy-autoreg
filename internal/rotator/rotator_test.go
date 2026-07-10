package rotator

import (
        "context"
        "errors"
        "testing"
        "time"

        "grok-desktop/internal/store"
)

// helper to spin up an in-memory store with N accounts
func newTestStore(t *testing.T, n int) *store.Store {
        t.Helper()
        s, err := store.Open(t.TempDir())
        if err != nil {
                t.Fatalf("open store: %v", err)
        }
        for i := 0; i < n; i++ {
                acc := store.Account{
                        ID:          "acc-" + string(rune('a'+i)),
                        Label:       "Account " + string(rune('A'+i)),
                        Email:       string(rune('a'+i)) + "@example.com",
                        AccessToken: "tok-" + string(rune('a'+i)),
                        CreatedAt:   time.Now().Add(-time.Duration(i) * time.Hour),
                }
                if err := s.UpsertAccount(acc); err != nil {
                        t.Fatalf("upsert account: %v", err)
                }
        }
        return s
}

func TestIsLimitError(t *testing.T) {
        cases := []struct {
                err  error
                want bool
        }{
                {nil, false},
                {errors.New("not found"), false},
                {errors.New("HTTP 429: rate limit exceeded"), true},
                {errors.New("rate_limit exceeded"), true},
                {errors.New("too many requests"), true},
                {errors.New("HTTP 402: payment required"), true},
                {errors.New("free tier limit reached"), true},
                {errors.New("quota exhausted"), true},
                {errors.New("usage_limit_reached"), true},
                {errors.New("insufficient_quota"), true},
                {errors.New("connection reset"), false},
        }
        for _, c := range cases {
                got := IsLimitError(c.err)
                if got != c.want {
                        t.Errorf("IsLimitError(%v) = %v, want %v", c.err, got, c.want)
                }
        }
}

func TestMarkLimitedAndIsLimited(t *testing.T) {
        s := newTestStore(t, 2)
        r := New(s)
        r.SetAutoRotate(true)
        if r.IsLimited("acc-a") {
                t.Error("acc-a should not be limited initially")
        }
        r.MarkLimited("acc-a", "rate_limit", 100*time.Millisecond)
        if !r.IsLimited("acc-a") {
                t.Error("acc-a should be limited after MarkLimited")
        }
        time.Sleep(150 * time.Millisecond)
        if r.IsLimited("acc-a") {
                t.Error("acc-a should not be limited after cooldown expires")
        }
}

func TestPickNextAvailable(t *testing.T) {
        s := newTestStore(t, 3)
        r := New(s)
        // active is acc-a (first created); exclude it → should pick next oldest
        r.SetAutoRotate(true)
        next, ok := r.PickNextAvailable("acc-a")
        if !ok {
                t.Fatal("expected a next available account")
        }
        if next == "acc-a" {
                t.Error("should not return the excluded account")
        }
        // limit all but one
        r.MarkLimited("acc-a", "rate_limit", time.Hour)
        r.MarkLimited("acc-b", "rate_limit", time.Hour)
        next, ok = r.PickNextAvailable("acc-a")
        if !ok {
                t.Fatal("expected acc-c to be available")
        }
        if next != "acc-c" {
                t.Errorf("expected acc-c, got %s", next)
        }
        // limit all → no fresh accounts
        r.MarkLimited("acc-c", "rate_limit", time.Hour)
        _, ok = r.PickNextAvailable("acc-a")
        if ok {
                t.Error("expected no available account when all are limited")
        }
}

func TestRunWithRotation_Success(t *testing.T) {
        s := newTestStore(t, 3)
        r := New(s)
        r.SetAutoRotate(true)
        calls := 0
        err := r.RunWithRotation(context.Background(), func(ctx context.Context, accountID string) (Result, error) {
                calls++
                return Result{}, nil
        })
        if err != nil {
                t.Fatalf("unexpected error: %v", err)
        }
        if calls != 1 {
                t.Errorf("expected 1 call, got %d", calls)
        }
}

func TestRunWithRotation_RotatesOnLimit(t *testing.T) {
        s := newTestStore(t, 3)
        r := New(s)
        r.SetAutoRotate(true)
        calls := 0
        var seenAccounts []string
        err := r.RunWithRotation(context.Background(), func(ctx context.Context, accountID string) (Result, error) {
                calls++
                seenAccounts = append(seenAccounts, accountID)
                if calls == 1 {
                        // first account hits a rate limit
                        return Result{Retry: true, Reason: "rate_limit"}, errors.New("HTTP 429: rate limit exceeded")
                }
                // second account succeeds
                return Result{}, nil
        })
        if err != nil {
                t.Fatalf("unexpected error: %v", err)
        }
        if calls != 2 {
                t.Errorf("expected 2 calls, got %d", calls)
        }
        if len(seenAccounts) != 2 {
                t.Fatalf("expected 2 accounts seen, got %d", len(seenAccounts))
        }
        if seenAccounts[0] == seenAccounts[1] {
                t.Error("expected rotation to a different account")
        }
}

func TestRunWithRotation_AllLimited(t *testing.T) {
        s := newTestStore(t, 2)
        r := New(s)
        r.SetAutoRotate(true)
        calls := 0
        err := r.RunWithRotation(context.Background(), func(ctx context.Context, accountID string) (Result, error) {
                calls++
                return Result{Retry: true, Reason: "rate_limit"}, errors.New("HTTP 429: rate limit exceeded")
        })
        if err == nil {
                t.Fatal("expected error when all accounts are limited")
        }
        if calls < 2 {
                t.Errorf("expected at least 2 calls, got %d", calls)
        }
}

func TestRunWithRotation_NoRotate(t *testing.T) {
        s := newTestStore(t, 3)
        r := New(s)
        r.SetAutoRotate(false)
        calls := 0
        err := r.RunWithRotation(context.Background(), func(ctx context.Context, accountID string) (Result, error) {
                calls++
                return Result{Retry: true, Reason: "rate_limit"}, errors.New("HTTP 429: rate limit exceeded")
        })
        if err == nil {
                t.Fatal("expected error from fn (no rotation should happen)")
        }
        if calls != 1 {
                t.Errorf("expected 1 call with rotation disabled, got %d", calls)
        }
}

func TestCooldownForError(t *testing.T) {
        if got := CooldownForError(errors.New("HTTP 429: rate limit")); got != DefaultCooldown {
                t.Errorf("expected DefaultCooldown for 429, got %v", got)
        }
        if got := CooldownForError(errors.New("HTTP 402: payment required")); got != DailyCooldown {
                t.Errorf("expected DailyCooldown for 402, got %v", got)
        }
        if got := CooldownForError(errors.New("daily quota exhausted")); got != DailyCooldown {
                t.Errorf("expected DailyCooldown for daily quota, got %v", got)
        }
        if got := CooldownForError(nil); got != DefaultCooldown {
                t.Errorf("expected DefaultCooldown for nil, got %v", got)
        }
}
