package agent

import (
	"context"
	"encoding/json"
)

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && value[maxBytes]&0xc0 == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

// ObserveWithBudget returns a page observation constrained to approximately
// the given token budget. Content is prioritized: interactive elements first,
// then headings, then main content. 1 token ≈ 4 characters.
func (s *Session) ObserveWithBudget(budget int) (*Observation, error) {
	return s.observeWithBudget(budget, nil)
}

// ObserveWithBudgetContext captures a budgeted snapshot under ctx.
func (s *Session) ObserveWithBudgetContext(ctx context.Context, budget int) (*Observation, error) {
	return s.observeWithBudget(budget, ctx)
}

func (s *Session) observeWithBudget(budget int, ctx context.Context) (*Observation, error) {
	if budget < 1 {
		budget = 1
	}
	if budget > 5000 {
		budget = 5000
	}
	if ctx != nil {
		if err := s.mu.LockContext(ctx); err != nil {
			return nil, err
		}
	} else {
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if ctx != nil {
		if err := s.ensurePageContext(ctx); err != nil {
			return nil, err
		}
	} else if err := s.ensurePage(); err != nil {
		return nil, err
	}

	// Temporarily adjust content options for this observation
	savedOpts := s.contentOpts
	defer func() { s.contentOpts = savedOpts }()

	charBudget := budget * 4
	s.contentOpts.MaxLength = min(charBudget, s.contentOpts.MaxLength)

	// Allocate budget: 40% interactive, 10% headings, 35% text, 15% misc
	interactiveBudget := budget * 40 / 100
	textBudget := budget * 35 / 100

	// Cap interactive elements based on budget (~30 tokens per element)
	limits := budgetedObservationLimits(interactiveBudget, s.contentOpts)
	s.contentOpts.MaxLinks = limits.links
	s.contentOpts.MaxInputs = limits.inputs
	s.contentOpts.MaxButtons = limits.buttons

	evaluate := s.evaluateInFrame
	if ctx != nil {
		evaluate = func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) }
	}
	obs, err := s.observeInternalWithEvaluatorAndLimits(evaluate, &limits)
	if err != nil {
		return nil, err
	}

	// Truncate text to remaining budget
	budgetTextTruncated := false
	if len(obs.Text) > textBudget*4 {
		obs.Text = truncateUTF8Bytes(obs.Text, textBudget*4)
		obs.TextTruncated = true
		obs.Truncated = true
		budgetTextTruncated = true
	}
	obs.Budget = budget
	obs.BudgetTruncated = budgetTextTruncated || observationBudgetLimitsOmitted(obs, limits, savedOpts)
	obs.BudgetUsed = 1 // ensure the reported field is included in its own estimate
	obs.BudgetUsed = stableObservationTokenEstimate(obs)
	for obs.BudgetUsed > budget {
		obs.BudgetTruncated = true
		obs.Truncated = true
		obs.BudgetUsed = stableObservationTokenEstimate(obs)
		if obs.BudgetUsed <= budget {
			break
		}
		if !trimObservationForBudget(obs) {
			break
		}
		obs.BudgetUsed = stableObservationTokenEstimate(obs)
	}
	if obs.BudgetUsed > budget {
		// The fixed metadata alone can exceed very small budgets. Keep the
		// target identity and counts for callers, but report the actual estimate.
		obs.BudgetTruncated = true
		obs.Truncated = true
	}

	return obs, nil
}

func observationBudgetLimitsOmitted(obs *Observation, limits observationLimits, configured ContentOptions) bool {
	if obs == nil {
		return false
	}
	maxLinks, maxInputs, maxButtons := configured.MaxLinks, configured.MaxInputs, configured.MaxButtons
	if maxLinks <= 0 {
		maxLinks = 25
	}
	if maxInputs <= 0 {
		maxInputs = 20
	}
	if maxButtons <= 0 {
		maxButtons = 15
	}
	return limits.links < maxLinks && obs.LinksTotal > len(obs.Links) ||
		limits.inputs < maxInputs && obs.InputsTotal > len(obs.Inputs) ||
		limits.buttons < maxButtons && obs.ButtonsTotal > len(obs.Buttons)
}

func stableObservationTokenEstimate(obs *Observation) int {
	if obs == nil {
		return 0
	}
	if obs.BudgetUsed == 0 {
		obs.BudgetUsed = 1
	}
	for {
		estimate := observationTokenEstimate(obs)
		if estimate == obs.BudgetUsed {
			return estimate
		}
		obs.BudgetUsed = estimate
	}
}

func observationTokenEstimate(obs *Observation) int {
	if obs == nil {
		return 0
	}
	encoded, err := json.Marshal(obs)
	if err != nil {
		return 0
	}
	// JSON is the actual MCP payload shape; 4 bytes per token is the same
	// conservative approximation used by the observation allocator.
	return (len(encoded) + 3) / 4
}

func trimObservationForBudget(obs *Observation) bool {
	if obs.Meta != nil {
		obs.Meta = nil
		return true
	}
	if len(obs.Text) > 0 {
		obs.Text = truncateUTF8Bytes(obs.Text, len(obs.Text)*3/4)
		obs.TextTruncated = true
		return true
	}
	if n := len(obs.Links); n > 0 {
		obs.Links = obs.Links[:n-1]
		return true
	}
	if n := len(obs.Inputs); n > 0 {
		obs.Inputs = obs.Inputs[:n-1]
		return true
	}
	if n := len(obs.Buttons); n > 0 {
		obs.Buttons = obs.Buttons[:n-1]
		return true
	}
	if n := len(obs.ActiveNavigation); n > 0 {
		obs.ActiveNavigation = obs.ActiveNavigation[:n-1]
		return true
	}
	if obs.ActiveTabID != "" {
		obs.ActiveTabID = ""
		return true
	}
	if obs.ActiveTab != "" {
		obs.ActiveTab = ""
		return true
	}
	return false
}

func budgetedObservationLimits(interactiveBudget int, opts ContentOptions) observationLimits {
	const tokensPerElement = 30
	return observationLimits{
		links:   min(interactiveBudget/tokensPerElement, opts.MaxLinks),
		inputs:  min(interactiveBudget/(tokensPerElement*2), opts.MaxInputs),
		buttons: min(interactiveBudget/(tokensPerElement*2), opts.MaxButtons),
	}
}

// EstimateTokens returns an approximate token count for a string.
// Uses the heuristic of 1 token ≈ 4 characters.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
