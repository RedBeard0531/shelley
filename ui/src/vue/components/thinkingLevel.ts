// Shared thinking-level constants/types. "default" leaves the request unset so
// the selected model's configured/provider default applies.
export type ThinkingLevel =
  | "default"
  | "off"
  | "minimal"
  | "low"
  | "medium"
  | "high"
  | "xhigh"
  | "max";

export const DEFAULT_THINKING_LEVEL: ThinkingLevel = "default";

export const THINKING_LEVELS: { value: ThinkingLevel; label: string }[] = [
  { value: "default", label: "default" },
  { value: "off", label: "off" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

export const CONCRETE_THINKING_LEVELS = THINKING_LEVELS.filter(
  (level): level is { value: Exclude<ThinkingLevel, "default">; label: string } =>
    level.value !== "default",
).map((level) => level.value);

// supportedThinkingLevels returns the levels a user may explicitly pick for a
// model: the advertised list when known, none when reasoning is unsupported,
// else the standard set through xhigh (max must be advertised).
export function supportedThinkingLevels(
  model: ReasoningModelCapabilities | undefined,
): readonly Exclude<ThinkingLevel, "default">[] {
  if (model?.supports_reasoning === false) return [];
  if (model?.reasoning_levels?.length) return model.reasoning_levels;
  return CONCRETE_THINKING_LEVELS.filter((level) => level !== "max");
}

// roundThinkingLevel rounds an unsupported level to a supported one,
// preferring a HIGHER effort: an exact match wins, else the closest supported
// level above, else the closest supported level below. Rounding up keeps a
// user's intent from silently degrading when a model advertises a sparser
// list (e.g. xhigh lands on max, not on high). Off is a mode rather than an
// effort tier: non-off values never round to off.
export function roundThinkingLevel(
  level: Exclude<ThinkingLevel, "default">,
  supported: readonly Exclude<ThinkingLevel, "default">[],
): Exclude<ThinkingLevel, "default"> {
  if (supported.length === 0 || supported.includes(level)) return level;
  const candidates = CONCRETE_THINKING_LEVELS.filter(
    (candidate) => supported.includes(candidate) && (level === "off" || candidate !== "off"),
  );
  if (candidates.length === 0) return level;
  const target = CONCRETE_THINKING_LEVELS.indexOf(level);
  const higher = CONCRETE_THINKING_LEVELS.filter(
    (candidate) => candidates.includes(candidate) && CONCRETE_THINKING_LEVELS.indexOf(candidate) > target,
  );
  if (higher.length > 0) {
    return higher.reduce((best, candidate) =>
      CONCRETE_THINKING_LEVELS.indexOf(candidate) < CONCRETE_THINKING_LEVELS.indexOf(best)
        ? candidate
        : best,
    );
  }
  const lower = candidates.filter(
    (candidate) => CONCRETE_THINKING_LEVELS.indexOf(candidate) < target,
  );
  if (lower.length > 0) {
    return lower.reduce((best, candidate) =>
      CONCRETE_THINKING_LEVELS.indexOf(candidate) > CONCRETE_THINKING_LEVELS.indexOf(best)
        ? candidate
        : best,
    );
  }
  return level;
}

export interface ReasoningModelCapabilities {
  supports_reasoning?: boolean;
  reasoning_levels?: Exclude<ThinkingLevel, "default">[];
}

// isToggleOnlyReasoning reports whether a model reasons but advertises no
// effort list, i.e. its reasoning is a bare on/off toggle.
function isToggleOnlyReasoning(
  model: ReasoningModelCapabilities | undefined,
): model is ReasoningModelCapabilities {
  return (
    model !== undefined &&
    model.supports_reasoning === true &&
    !model.reasoning_levels?.length
  );
}

// normalizeThinkingLevelForModel translates a stored reasoning level when the
// active model changes. source is the model being left (undefined when
// unknown); model is the model becoming active.
//
// Preferring a HIGHER effort: an exact match wins, then the closest supported
// level above, then the closest below. A bare "on" (the "default"/auto state)
// carried over from a reasoning toggle (a reasoning model with no advertised
// effort list) names itself as "high" before clamping, so it lands in the
// target's list. Toggle-only targets can't express efforts: any generic level
// collapses to bare "on" regardless of its source; "off" stays meaningful.
export function normalizeThinkingLevelForModel(
  level: ThinkingLevel,
  model: ReasoningModelCapabilities | undefined,
  source?: ReasoningModelCapabilities | undefined,
): ThinkingLevel {
  if (!model || model.supports_reasoning === false) return "default";
  if (model.reasoning_levels?.length) {
    if (level === "default") {
      // Bare "on" picked up on a toggle-only model: name it as high before
      // landing in this model's effort list.
      if (isToggleOnlyReasoning(source)) {
        const rounded = roundThinkingLevel("high", model.reasoning_levels);
        return model.reasoning_levels.includes(rounded) ? rounded : "default";
      }
      return "default";
    }
    const rounded = roundThinkingLevel(level, model.reasoning_levels);
    // Rounding can fail to land on a supported level (e.g. an off-only
    // model never attracts non-off levels); reset rather than send a level
    // the server would reject.
    return model.reasoning_levels.includes(rounded) ? rounded : "default";
  }
  // Toggle-only target: efforts aren't expressible. Any generic level is a
  // bare "on" regardless of where it came from; off stays meaningful.
  return level === "off" ? "off" : "default";
}

export const THINKING_LEVEL_KEY = "shelley.thinkingLevel.v2";

// storedThinkingLevel is the user's last composer effort pick, or the
// "default" sentinel when nothing valid is stored.
export function storedThinkingLevel(): ThinkingLevel {
  const stored = localStorage.getItem(THINKING_LEVEL_KEY);
  return THINKING_LEVELS.some((l) => l.value === stored)
    ? (stored as ThinkingLevel)
    : DEFAULT_THINKING_LEVEL;
}
