package oai

import "shelley.exe.dev/models/modelsdev"

// ReasoningReplay controls whether and how prior assistant reasoning is sent
// back to OpenAI-compatible APIs during tool loops.
type ReasoningReplay string

const (
	ReasoningReplayAuto    ReasoningReplay = "auto"
	ReasoningReplayNone    ReasoningReplay = "none"
	ReasoningReplayContent ReasoningReplay = "reasoning_content"
)

// ResolveReasoningReplay resolves auto from models.dev while preserving
// explicit none. The zero value is auto for service configuration.
func ResolveReasoningReplay(endpoint, modelName string, configured ReasoningReplay) ReasoningReplay {
	switch configured {
	case "", ReasoningReplayAuto:
		field, found := modelsdev.LookupInterleavedReasoningField(endpoint, modelName)
		if field == string(ReasoningReplayContent) {
			return ReasoningReplayContent
		}
		if found {
			return ReasoningReplayNone
		}
		return ""
	case ReasoningReplayNone, ReasoningReplayContent:
		return configured
	default:
		return ReasoningReplayNone
	}
}
