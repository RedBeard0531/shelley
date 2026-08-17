import {
  normalizeThinkingLevelForModel,
  roundThinkingLevel,
  supportedThinkingLevels,
} from "./thinkingLevel";

let passed = 0;
let failed = 0;

function expectRound(
  level: Parameters<typeof roundThinkingLevel>[0],
  supported: Parameters<typeof roundThinkingLevel>[1],
  want: ReturnType<typeof roundThinkingLevel>,
) {
  const got = roundThinkingLevel(level, supported);
  if (got === want) {
    passed++;
  } else {
    failed++;
    console.error(
      `FAIL: roundThinkingLevel(${level}, ${supported.join(",")}) = ${got}, want ${want}`,
    );
  }
}

expectRound("high", ["low", "high"], "high");
expectRound("max", ["off", "high", "xhigh"], "xhigh");
expectRound("xhigh", ["high", "max"], "max");
expectRound("medium", ["high", "max"], "high");
expectRound("minimal", ["off", "low"], "low");
expectRound("off", ["low", "high"], "low");

function expectModelLevel(
  level: Parameters<typeof normalizeThinkingLevelForModel>[0],
  model: Parameters<typeof normalizeThinkingLevelForModel>[1],
  want: ReturnType<typeof normalizeThinkingLevelForModel>,
  source?: Parameters<typeof normalizeThinkingLevelForModel>[2],
) {
  const got = normalizeThinkingLevelForModel(level, model, source);
  if (got === want) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: normalizeThinkingLevelForModel(${level}) = ${got}, want ${want}`);
  }
}

expectModelLevel(
  "max",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "xhigh"] },
  "xhigh",
);
expectModelLevel("minimal", { supports_reasoning: true, reasoning_levels: ["off", "low"] }, "low");
expectModelLevel("high", { supports_reasoning: true, reasoning_levels: ["off"] }, "default");
expectModelLevel("high", { supports_reasoning: false }, "default");
expectModelLevel("max", { supports_reasoning: true }, "default");
expectModelLevel("xhigh", { supports_reasoning: true }, "default");
// Rounding prefers the closest HIGHER level.
expectModelLevel(
  "xhigh",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "max"] },
  "max",
);
expectModelLevel(
  "medium",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "max"] },
  "high",
);
// Bare "on" (default/auto) carried over from a toggle-only source names
// itself as high before landing in the target's effort list.
expectModelLevel(
  "default",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "max"] },
  "high",
  { supports_reasoning: true },
);
expectModelLevel(
  "default",
  { supports_reasoning: true, reasoning_levels: ["off", "max"] },
  "max",
  { supports_reasoning: true },
);
// Bare "on" from an effort-list source stays a bare "on".
expectModelLevel(
  "default",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "max"] },
  "default",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "max"] },
);
// Any effort collapses to bare "on" on a toggle-only target, regardless of
// its source; off stays meaningful.
expectModelLevel("max", { supports_reasoning: true }, "default", {
  supports_reasoning: true,
  reasoning_levels: ["off", "high", "max"],
});
expectModelLevel("xhigh", { supports_reasoning: true }, "default", {
  supports_reasoning: true,
  reasoning_levels: ["off", "high", "max"],
});
expectModelLevel("off", { supports_reasoning: true }, "off", {
  supports_reasoning: true,
  reasoning_levels: ["off", "high", "max"],
});
expectModelLevel("xhigh", { supports_reasoning: true }, "default", {
  supports_reasoning: true,
});

function expectSupported(
  model: Parameters<typeof supportedThinkingLevels>[0],
  want: readonly string[],
) {
  const got = supportedThinkingLevels(model);
  if (got.join(",") === want.join(",")) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: supportedThinkingLevels = ${got.join(",")}, want ${want.join(",")}`);
  }
}

expectSupported({ supports_reasoning: true, reasoning_levels: ["off", "high", "max"] }, [
  "off",
  "high",
  "max",
]);
expectSupported({ supports_reasoning: false }, []);
expectSupported({ supports_reasoning: true }, ["off", "minimal", "low", "medium", "high", "xhigh"]);
expectSupported(undefined, ["off", "minimal", "low", "medium", "high", "xhigh"]);

if (failed > 0) process.exit(1);
console.log(`thinkingLevel: ${passed} passed`);
