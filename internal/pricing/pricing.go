package pricing

import "strings"

// Official-ish xAI public list prices (USD per 1M tokens).
// Source: docs.x.ai/developers/pricing — Grok 4.5: $2 in / $0.50 cached / $6 out.
type Rate struct {
	InputPerM  float64 `json:"input_per_m"`
	CachedPerM float64 `json:"cached_per_m"`
	OutputPerM float64 `json:"output_per_m"`
	Label      string  `json:"label"`
}

var table = map[string]Rate{
	"grok-4.5": {
		InputPerM: 2.00, CachedPerM: 0.50, OutputPerM: 6.00, Label: "Grok 4.5",
	},
	"grok-4.5-build": {
		InputPerM: 2.00, CachedPerM: 0.50, OutputPerM: 6.00, Label: "Grok 4.5",
	},
	"grok-composer-2.5-fast": {
		// Composer via CLI proxy — treat like mid-tier; no public SKU → estimate as 4.5 rates
		InputPerM: 2.00, CachedPerM: 0.50, OutputPerM: 6.00, Label: "Composer 2.5 Fast (est.)",
	},
}

// Default when unknown model
var fallback = Rate{InputPerM: 2.00, CachedPerM: 0.50, OutputPerM: 6.00, Label: "Default (Grok 4.5 rates)"}

func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimSuffix(m, "-responses")
	m = strings.TrimSuffix(m, "@responses")
	m = strings.TrimSuffix(m, "/responses")
	return m
}

func RateFor(model string) Rate {
	m := NormalizeModel(model)
	if r, ok := table[m]; ok {
		return r
	}
	// prefix match
	for k, r := range table {
		if strings.HasPrefix(m, k) || strings.Contains(m, k) {
			return r
		}
	}
	return fallback
}

// CostUSD estimates billable cost. Reasoning tokens are billed as output on xAI.
func CostUSD(model string, prompt, completion, reasoning, cached int64) float64 {
	r := RateFor(model)
	in := float64(prompt-cached) * r.InputPerM / 1_000_000
	if in < 0 {
		in = float64(prompt) * r.InputPerM / 1_000_000
	}
	cache := float64(cached) * r.CachedPerM / 1_000_000
	// xAI reports reasoning separately in completion_tokens_details while
	// completion_tokens is often only the visible answer — bill both as output.
	outTokens := completion + reasoning
	out := float64(outTokens) * r.OutputPerM / 1_000_000
	return in + cache + out
}

func AllRates() map[string]Rate {
	out := map[string]Rate{}
	for k, v := range table {
		out[k] = v
	}
	return out
}
