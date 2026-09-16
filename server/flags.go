package server

import "shelley.exe.dev/featureflags"

// FlagReflectionEmojiFavicon, when enabled, replaces the generated "Cool S"
// favicon with the VM's emoji taken from the exe.dev reflection integration's
// root document. When false, the deterministic per-host Shelley mark is used
// instead. This flag is evaluated server-side, in serveIndexWithInit, because
// the favicon <link> is injected into index.html as the page is served.
// Defaults on; falls back to the generated mark when reflection has no emoji.
var FlagReflectionEmojiFavicon = featureflags.Register(featureflags.Flag{
	Name:        "reflection-emoji-favicon",
	Description: "Use the VM's reflection emoji as the browser favicon instead of the generated Shelley mark.",
	Default:     true,
})

// FlagPerformanceHUD overlays a small heads-up display in the web UI showing
// live counters of hot reactive recomputations (message coalescing, render
// model rebuilds, markdown parses, scroll/resize handler fires, store
// notifications, ...). The counters themselves are always collected — they
// are plain Map increments, cheap enough to leave on — and are accessible
// from the browser console via window.__shelleyPerf regardless of the flag.
// The flag only controls whether the HUD overlay renders.
var FlagPerformanceHUD = featureflags.Register(featureflags.Flag{
	Name:        "performance-hud",
	Description: "Show a heads-up display of UI recomputation counters (also available via __shelleyPerf in the console).",
	Default:     false,
})

// FlagCompactSendThresholds defaults the composer to Compact and send once
// the context reaches 200k tokens.
var FlagCompactSendThresholds = featureflags.Register(featureflags.Flag{
	Name:        "compact-send-thresholds",
	Description: "Default the composer to Compact and send once the context reaches 200k tokens.",
	Default:     false,
})

// FlagPatchSimple switches the patch tool from its full nested patches schema
// to a simplified path-and-edits replacement schema.
var FlagPatchSimple = featureflags.Register(featureflags.Flag{
	Name:        "patch-simple",
	Description: "Use a simplified path and edits array for atomic exact-text replacements. When off, use the full nested patches schema.",
	Default:     false,
})

// FlagPatchOpenAIRaw lets capable direct OpenAI Responses models use the raw,
// grammar-constrained Codex apply_patch tool. It overrides patch-simple when
// both flags are enabled and has no effect on unsupported providers.
var FlagPatchOpenAIRaw = featureflags.Register(featureflags.Flag{
	Name:        "patch-openai-raw",
	Description: "Use raw grammar-constrained apply_patch for capable direct OpenAI Responses models, overriding the full or simplified nested patch schema.",
	Default:     false,
})
