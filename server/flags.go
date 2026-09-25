package server

import "shelley.exe.dev/featureflags"

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

// FlagPatchSimple selects simplified edits for models without native apply_patch:
// a single modification per call (one exact-text replace or one EOF append).
// When off, use the complex one-operation schema (replace, replace_all,
// append_eof, prepend_bof, overwrite). Capable OpenAI Responses models use native
// apply_patch regardless of this flag.
var FlagPatchSimple = featureflags.Register(featureflags.Flag{
	Name:        "patch-simple",
	Description: "For models without native apply_patch, use a simplified single-modification schema (one exact-text replace or one EOF append per call). When off, use the complex one-operation schema.",
	Default:     false,
})
