import type { Point } from "./contextComposition";
import {
  categoryHint,
  categoryTokens,
  contextCategories,
  contextLayers,
  contextSegmentStarts,
} from "./contextCategories";

let failed = 0;
function assert(cond: boolean, msg: string) {
  if (!cond) {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

const point = (parts: Record<string, number>, segment = 0): Point => ({
  total: Object.values(parts).reduce((a, b) => a + b, 0),
  segment,
  parts,
  args: { "repo/read": 3000 },
  toolBreakdown: { "repo/read": { "a.go": 3000, "b.go": 1000 } },
});
const points = [
  point({ user: 100, assistant: 50 }),
  point({ user: 100, assistant: 80, reasoning: 20, "repo/read": 4000, "bash:other": 10 }, 0),
  point({ user: 30, "bash:other": 5 }, 1),
];

const cats = contextCategories(points);
assert(
  cats.map((c) => c.key).join() === "user,assistant,reasoning,repo/read,bash:other",
  `categories in stacking order: ${cats.map((c) => c.key)}`,
);
assert(contextCategories([]).length === 0, "no points, no categories");

assert(categoryTokens(points[1], "user") === 100, "role bands read their own part");
assert(categoryTokens(points[1], "reasoning") === 20, "reasoning is its own band");
assert(categoryTokens(points[1], "repo/read") === 4000, "other categories read their own part");
assert(categoryTokens(points[0], "repo/read") === 0, "missing parts are zero");

const layers = contextLayers(points, cats);
assert(layers.length === 5 && layers[0].length === 3, "a layer per category, a value per call");
assert(layers[0].join() === "100,100,30", `bottom layer is its own tokens: ${layers[0]}`);
assert(layers[1].join() === "150,180,30", `layers stack: ${layers[1]}`);
assert(layers[2].join() === "150,200,30", `reasoning stacks onto the roles: ${layers[2]}`);
assert(layers[4].join() === "150,4210,35", `top layer is the plotted total: ${layers[4]}`);

assert(contextSegmentStarts(points).join() === "2", "compaction starts a segment");
assert(contextSegmentStarts(points.slice(0, 2)).length === 0, "no compaction, no starts");

assert(
  categoryHint("reasoning", points[1]).includes("reasoning tokens"),
  "reasoning hint names the exact split",
);
assert(categoryHint("repo/read", points[1]) === "a.go 3.0k · b.go 1.0k", "tool hint, biggest first");
assert(categoryHint("bash:other", points[1]) === "Tool output", "tool hint without breakdown");

if (failed) process.exit(1);
console.log("✓ context categories");
