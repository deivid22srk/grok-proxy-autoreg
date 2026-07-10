// Package rotator implements automatic multi-account rotation when the active
// account hits a free-tier / rate-limit on the upstream xAI API.
//
// The rotator is consumer-agnostic: it only knows about account IDs and a
// cooldown window. The actual HTTP request execution is delegated to a
// caller-provided function, which receives the account ID to use and reports
// whether the response should trigger a rotation.
//
// Usage pattern:
//
//	r := rotator.New(store)
//	err := r.RunWithRotation(ctx, func(ctx context.Context, accountID string) (rotator.Result, error) {
//	    // make the upstream request with this account
//	    // return Result{Retry: true, Reason: "429"} if rate-limited
//	    // return Result{Retry: false} on success or hard error
//	})
package rotator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"grok-desktop/internal/store"
)

// DefaultCooldown is how long an account stays marked as limited before it is
// eligible again. xAI free-tier daily limits typically reset on a rolling
// 24h window, but per-request rate limits (429) usually clear in seconds.
// We use a conservative default that callers can override.
const DefaultCooldown = 5 * time.Minute

// DailyCooldown is used when the upstream explicitly says the daily quota
// was hit (e.g. "daily limit", "quota_exceeded").
const DailyCooldown = 6 * time.Hour

// Result tells the rotator whether to retry with another account.
type Result struct {
	// Retry=true means "this account hit a limit, please rotate and try again".
	Retry bool
	// Reason is a short human-readable label (e.g. "429", "rate_limit",
	// "daily_quota", "payment_required"). Logged for visibility.
	Reason string
	// Cooldown override; if zero, DefaultCooldown is used.
	Cooldown time.Duration
}

// AccountStatus tracks per-account limit state.
type AccountStatus struct {
	// LimitedUntil is the time after which the account is eligible again.
	// Zero means "available".
	LimitedUntil time.Time
	// Reason why it was limited.
	Reason string
	// ConsecutiveHits counts how many times this account hit a limit recently.
	ConsecutiveHits int
}

// Rotator coordinates account rotation across concurrent requests.
type Rotator struct {
	store *store.Store

	mu      sync.Mutex
	status  map[string]*AccountStatus
	cooled  map[string]*AccountStatus // alias to status
	auto    bool
	verbose bool
}

// New creates a Rotator backed by the given store.
func New(s *store.Store) *Rotator {
	return &Rotator{
		store:  s,
		status: map[string]*AccountStatus{},
	}
}

// SetAutoRotate enables or disables automatic rotation. When disabled, the
// rotator is a no-op and RunWithRotation just calls fn once with the active
// account.
func (r *Rotator) SetAutoRotate(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auto = v
}

// AutoRotate returns whether auto-rotation is enabled.
func (r *Rotator) AutoRotate() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auto
}

// SetVerbose enables logging of rotation decisions to stderr.
func (r *Rotator) SetVerbose(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verbose = v
}

// Verbose returns whether verbose mode is on.
func (r *Rotator) Verbose() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verbose
}

// MarkLimited marks the given account as limited for `cooldown` duration.
// If cooldown is zero, DefaultCooldown is used.
func (r *Rotator) MarkLimited(accountID string, reason string, cooldown time.Duration) {
	if accountID == "" {
		return
	}
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.status[accountID]
	if st == nil {
		st = &AccountStatus{}
		r.status[accountID] = st
	}
	st.LimitedUntil = time.Now().Add(cooldown)
	st.Reason = reason
	st.ConsecutiveHits++
	if r.verbose {
		fmt.Printf("[rotator] account %s marked limited (%s) for %s (hit #%d)\n",
			shortID(accountID), reason, cooldown, st.ConsecutiveHits)
	}
}

// MarkAvailable clears the limited state for an account (e.g. after a
// successful request).
func (r *Rotator) MarkAvailable(accountID string) {
	if accountID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.status[accountID]; ok {
		st.LimitedUntil = time.Time{}
		st.Reason = ""
		st.ConsecutiveHits = 0
	}
}

// IsLimited returns whether the account is currently in cooldown.
func (r *Rotator) IsLimited(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.status[accountID]
	if !ok || st.LimitedUntil.IsZero() {
		return false
	}
	if time.Now().After(st.LimitedUntil) {
		// expired — clear it
		st.LimitedUntil = time.Time{}
		st.Reason = ""
		return false
	}
	return true
}

// Status returns a snapshot of all account statuses (for the CLI/UI).
func (r *Rotator) Status() map[string]AccountStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]AccountStatus, len(r.status))
	for k, v := range r.status {
		cp := *v
		out[k] = cp
	}
	return out
}

// PickNextAvailable returns the next account ID that is not currently limited,
// preferring the current active account. If `exclude` is provided, that ID is
// skipped (used to avoid re-picking the account that just hit a limit).
//
// Returns ("", false) if no eligible account exists.
func (r *Rotator) PickNextAvailable(exclude string) (string, bool) {
	accs := r.store.ListAccounts()
	if len(accs) == 0 {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	// First pass: prefer the currently-active account if it's eligible.
	active := r.store.Settings().ActiveAccountID
	if active != "" && active != exclude {
		if st := r.status[active]; st == nil || st.LimitedUntil.IsZero() || now.After(st.LimitedUntil) {
			return active, true
		}
	}
	// Second pass: any non-limited account, deterministic order (oldest first).
	type cand struct {
		id      string
		created time.Time
	}
	cands := make([]cand, 0, len(accs))
	for _, a := range accs {
		if a.ID == exclude {
			continue
		}
		st := r.status[a.ID]
		if st != nil && !st.LimitedUntil.IsZero() && now.Before(st.LimitedUntil) {
			continue
		}
		cands = append(cands, cand{id: a.ID, created: a.CreatedAt})
	}
	if len(cands) == 0 {
		return "", false
	}
	// sort by created at
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].created.Before(cands[i].created) {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	return cands[0].id, true
}

// SwitchTo sets the given account as active in the store.
func (r *Rotator) SwitchTo(accountID string) error {
	if accountID == "" {
		return errors.New("empty account id")
	}
	if err := r.store.SetActiveAccount(accountID); err != nil {
		return err
	}
	if r.verbose {
		fmt.Printf("[rotator] switched active account -> %s\n", shortID(accountID))
	}
	return nil
}

// RunWithRotation executes fn(ctx, accountID). If fn returns Result.Retry=true
// and auto-rotation is enabled, it marks the account as limited, picks the
// next available account, switches to it, and retries fn. This continues
// until either fn succeeds (or hard-errors), or no more eligible accounts
// remain.
//
// The first call uses the currently active account. If auto-rotation is
// disabled, fn is called exactly once.
func (r *Rotator) RunWithRotation(ctx context.Context, fn func(ctx context.Context, accountID string) (Result, error)) error {
	active := r.store.Settings().ActiveAccountID
	if active == "" {
		if acc, ok := r.store.ActiveAccount(); ok {
			active = acc.ID
		}
	}
	if active == "" {
		return errors.New("nenhuma conta configurada — execute `grok-proxy-cli login`")
	}

	if !r.AutoRotate() {
		_, err := fn(ctx, active)
		return err
	}

	tried := map[string]bool{}
	current := active
	maxAttempts := 20 // safety bound
	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		tried[current] = true
		res, err := fn(ctx, current)
		if err != nil {
			// If fn errored AND the error looks like a rate limit, treat as retry.
			if r.AutoRotate() && IsLimitError(err) {
				cd := CooldownForError(err)
				r.MarkLimited(current, classifyError(err), cd)
				if next, ok := r.PickNextAvailable(current); ok && !tried[next] {
					if r.verbose {
						fmt.Printf("[rotator] account %s hit limit (%v) — rotating to %s\n",
							shortID(current), err, shortID(next))
					}
					if err := r.SwitchTo(next); err != nil {
						return err
					}
					current = next
					continue
				}
				// No fresh accounts left — try to wait on the least-cooled one.
				if next, ok := r.pickLeastCooled(tried); ok {
					if r.verbose {
						fmt.Printf("[rotator] no fresh account — retrying least-cooled %s\n", shortID(next))
					}
					if err := r.SwitchTo(next); err != nil {
						return err
					}
					current = next
					continue
				}
				return fmt.Errorf("todas as contas estão limitadas: %v", err)
			}
			return err
		}
		if !res.Retry {
			// success — clear any stale "limited" flag from a previous hit
			r.MarkAvailable(current)
			return nil
		}
		// Retry requested: mark limited and rotate.
		cd := res.Cooldown
		if cd <= 0 {
			cd = DefaultCooldown
		}
		r.MarkLimited(current, res.Reason, cd)
		if next, ok := r.PickNextAvailable(current); ok && !tried[next] {
			if r.verbose {
				fmt.Printf("[rotator] account %s hit limit (%s) — rotating to %s\n",
					shortID(current), res.Reason, shortID(next))
			}
			if err := r.SwitchTo(next); err != nil {
				return err
			}
			current = next
			continue
		}
		if next, ok := r.pickLeastCooled(tried); ok {
			if r.verbose {
				fmt.Printf("[rotator] no fresh account — retrying least-cooled %s\n", shortID(next))
			}
			if err := r.SwitchTo(next); err != nil {
				return err
			}
			current = next
			continue
		}
		return fmt.Errorf("conta %s limitada (%s) e nenhuma outra disponível", shortID(current), res.Reason)
	}
	return errors.New("rotator: excedeu o número máximo de tentativas")
}

// pickLeastCooled returns the account with the smallest remaining cooldown
// among all accounts (including ones already tried). Used as a last resort.
func (r *Rotator) pickLeastCooled(tried map[string]bool) (string, bool) {
	accs := r.store.ListAccounts()
	if len(accs) == 0 {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var bestID string
	var bestRemaining time.Duration = -1
	for _, a := range accs {
		st := r.status[a.ID]
		var remaining time.Duration
		if st == nil || st.LimitedUntil.IsZero() || now.After(st.LimitedUntil) {
			remaining = 0
		} else {
			remaining = st.LimitedUntil.Sub(now)
		}
		if bestRemaining < 0 || remaining < bestRemaining {
			bestRemaining = remaining
			bestID = a.ID
		}
	}
	return bestID, bestID != ""
}

// ---- error classification helpers ----

// IsLimitError returns true if the error message looks like a rate-limit or
// free-tier quota error from xAI.
func IsLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "http 429") || strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate_limit") || strings.Contains(msg, "too many requests") {
		return true
	}
	if strings.Contains(msg, "http 402") || strings.Contains(msg, "payment required") ||
		strings.Contains(msg, "quota") || strings.Contains(msg, "free tier") ||
		strings.Contains(msg, "free-tier") || strings.Contains(msg, "limit exceeded") ||
		strings.Contains(msg, "insufficient_quota") || strings.Contains(msg, "usage_limit_reached") ||
		strings.Contains(msg, "credit") || strings.Contains(msg, "billing") {
		return true
	}
	// xAI specific marker sometimes seen
	if strings.Contains(msg, "exhausted") || strings.Contains(msg, "throttl") {
		return true
	}
	return false
}

// classifyError returns a short reason label for the error.
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "http 429"), strings.Contains(msg, "rate_limit"),
		strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"):
		return "rate_limit"
	case strings.Contains(msg, "http 402"), strings.Contains(msg, "payment required"),
		strings.Contains(msg, "billing"), strings.Contains(msg, "credit"):
		return "payment_required"
	case strings.Contains(msg, "daily"), strings.Contains(msg, "quota"),
		strings.Contains(msg, "usage_limit_reached"):
		return "daily_quota"
	case strings.Contains(msg, "free tier"), strings.Contains(msg, "free-tier"):
		return "free_tier_limit"
	case strings.Contains(msg, "exhausted"):
		return "exhausted"
	default:
		return "limit"
	}
}

// CooldownForError returns a cooldown duration appropriate to the error.
// Daily-quota / payment-required errors get the longer DailyCooldown.
func CooldownForError(err error) time.Duration {
	if err == nil {
		return DefaultCooldown
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "daily") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "usage_limit_reached") || strings.Contains(msg, "http 402") ||
		strings.Contains(msg, "payment required") || strings.Contains(msg, "free tier") ||
		strings.Contains(msg, "free-tier") || strings.Contains(msg, "billing") {
		return DailyCooldown
	}
	return DefaultCooldown
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:8] + "…"
}
