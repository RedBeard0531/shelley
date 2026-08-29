package server

import (
	"encoding/json"
	"net/http"

	"shelley.exe.dev/models/modelsdev"
)

// handleModelCosts resolves pricing (USD per million tokens) for a batch of
// (model, url) pairs seen in a conversation's usage data. Models without
// pricing map to null.
func (s *Server) handleModelCosts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models []struct {
			Model string `json:"model"`
			URL   string `json:"url"`
		} `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	costs := make(map[string]*modelsdev.Cost, len(req.Models))
	for _, m := range req.Models {
		if m.Model == "" {
			continue
		}
		if c, found := modelsdev.LookupCost(m.URL, m.Model); found {
			costs[m.Model] = &c
		} else {
			costs[m.Model] = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"costs": costs})
}

// subagentUsageDTO is one subagent's folded usage. Nested subagent costs are
// attributed to their top-level subagent, so child subtotals sum to the
// parent's aggregate. EstimatedUsd is priced client-side modelsdev rates; the
// client re-prices from the model costs it already fetched.
type subagentUsageDTO struct {
	ConversationID string  `json:"conversation_id"`
	Slug           string  `json:"slug"`
	LLMCalls       int64   `json:"llm_calls"`
	EstimatedUsd   float64 `json:"estimated_usd"`
	ReportedUsd    float64 `json:"reported_usd"`
	UnpricedCalls  int64   `json:"unpriced_calls"`
}

// handleSubagentUsage aggregates LLM usage across a conversation's subagents
// (recursively) and prices it. The token-cost graph shows this as a separate
// "plus $X for subagents" line; subagent calls are not part of the graph.
// Descendants' indirect usage (other_usage_data entries) is included.
func (s *Server) handleSubagentUsage(w http.ResponseWriter, r *http.Request, conversationID string) {
	rows, err := s.db.GetSubagentUsage(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	otherRows, err := s.db.GetSubagentOtherUsage(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var resp struct {
		LLMCalls            int64              `json:"llm_calls"`
		EstimatedUsd        float64            `json:"estimated_usd"`
		ReportedUsd         float64            `json:"reported_usd"`
		UnpricedReportedUsd float64            `json:"unpriced_reported_usd"`
		UnpricedModels      []string           `json:"unpriced_models"`
		UnpricedCalls       int64              `json:"unpriced_calls"`
		Subagents           []subagentUsageDTO `json:"subagents"`
	}
	resp.UnpricedModels = []string{}
	fold := func(model, url string, llmCalls, in, cacheWrite, cacheRead, out int64, costUsd float64) {
		resp.LLMCalls += llmCalls
		resp.ReportedUsd += costUsd
		if c, found := modelsdev.LookupCost(url, model); found {
			resp.EstimatedUsd += float64(in)*c.Input/1e6 +
				float64(cacheWrite)*c.CacheWrite/1e6 +
				float64(cacheRead)*c.CacheRead/1e6 +
				float64(out)*c.Output/1e6
		} else {
			resp.UnpricedReportedUsd += costUsd
			resp.UnpricedModels = append(resp.UnpricedModels, model)
			resp.UnpricedCalls += llmCalls
		}
	}
	for _, row := range rows {
		model, url := "", ""
		if row.ModelName != nil {
			model = *row.ModelName
		}
		if row.LlmApiUrl != nil {
			url = *row.LlmApiUrl
		}
		fold(model, url, row.LlmCalls, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
	}
	for _, row := range otherRows {
		fold(row.ModelName, row.LlmApiUrl, row.LlmCalls, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
	}
	// Per-subagent breakdown: direct children only; each child's total folds
	// in its own descendants, so nested subagent costs are attributed to their
	// top-level subagent. The child subtotals sum to the parent aggregate.
	children, err := s.db.GetSubagents(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.Subagents = []subagentUsageDTO{}
	for _, child := range children {
		childRows, err := s.db.GetSubtreeUsage(r.Context(), child.ConversationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		childOther, err := s.db.GetSubagentOtherUsage(r.Context(), child.ConversationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var dto subagentUsageDTO
		dto.ConversationID = child.ConversationID
		if child.Slug != nil {
			dto.Slug = *child.Slug
		}
		childFold := func(model, url string, llmCalls, in, cacheWrite, cacheRead, out int64, costUsd float64) {
			dto.LLMCalls += llmCalls
			dto.ReportedUsd += costUsd
			if c, found := modelsdev.LookupCost(url, model); found {
				dto.EstimatedUsd += float64(in)*c.Input/1e6 +
					float64(cacheWrite)*c.CacheWrite/1e6 +
					float64(cacheRead)*c.CacheRead/1e6 +
					float64(out)*c.Output/1e6
			} else {
				dto.UnpricedCalls += llmCalls
			}
		}
		for _, row := range childRows {
			model, url := "", ""
			if row.ModelName != nil {
				model = *row.ModelName
			}
			if row.LlmApiUrl != nil {
				url = *row.LlmApiUrl
			}
			childFold(model, url, row.LlmCalls, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
		}
		for _, row := range childOther {
			childFold(row.ModelName, row.LlmApiUrl, row.LlmCalls, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
		}
		resp.Subagents = append(resp.Subagents, dto)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
