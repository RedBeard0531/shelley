package modelsdev

import "slices"

// ProviderModel is an exact endpoint/catalog match, not an alias or a capability guess.
type ProviderModel struct {
	Name, ReleaseDate string
	Chat, Images      bool
	OutputLimit       int
	Reasoning         ReasoningCapabilities
}

// These SDK-implied hosts are needed only for saved-provider admission.
var savedProviderHosts = map[string]string{
	"api.groq.com":    "groq",
	"api.mistral.ai":  "mistral",
	"api.together.ai": "togetherai",
}

func LookupProviderModel(endpoint, id string) (ProviderModel, bool) {
	data := load()
	host := hostOf(endpoint)
	providers := hostIndex[host]
	if catalog, ok := savedProviderHosts[host]; ok {
		providers = []providerEntry{data[catalog]}
	}
	var best providerEntry
	score := -1
	for _, p := range providers {
		n := commonPrefixLen(pathSegments(p.API), pathSegments(endpoint))
		if n > score || n == score && providerStableKey(p) < providerStableKey(best) {
			best, score = p, n
		}
	}
	m, ok := best.Models[id]
	if !ok {
		return ProviderModel{}, false
	}
	return ProviderModel{
		Name: m.Name, ReleaseDate: m.ReleaseDate,
		Chat:        m.ToolCall && slices.Contains(m.Modalities.Input, "text") && slices.Contains(m.Modalities.Output, "text"),
		Images:      slices.Contains(m.Modalities.Input, "image"),
		OutputLimit: m.Limit.Output, Reasoning: parseReasoningCapabilities(m),
	}, true
}
