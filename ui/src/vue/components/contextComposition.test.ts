import type { Message } from "../../types";
import { contextCompositionPoints } from "./contextComposition";

let failed = 0;
function assert(cond: boolean, msg: string) {
  if (!cond) {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

let seq = 0;
function msg(type: string, content: object[], extra: Partial<Message> = {}): Message {
  seq++;
  return {
    message_id: `m${seq}`,
    conversation_id: "c",
    sequence_id: seq,
    type,
    generation: 1,
    created_at: "",
    llm_data: JSON.stringify({ Content: content }),
    ...extra,
  } as unknown as Message;
}
const usage = (tokens: number) => ({ usage_data: JSON.stringify({ input_tokens: tokens }) });
const big = "x".repeat(40_000); // ~10k tokens

const messages = [
  msg("user", [{ Type: 2, Text: "read it" }]),
  msg(
    "agent",
    [{ Type: 5, ID: "t1", ToolName: "bash", ToolInput: { command: "cat f" } }],
    usage(100),
  ),
  msg("user", [{ Type: 6, ToolUseID: "t1", ToolResult: [{ Type: 2, Text: big }] }]),
  msg("user", [{ Type: 2, Text: "Context is 10k." }]),
  msg("agent", [{ Type: 5, ID: "t2", ToolName: "compact_in_place", ToolInput: {} }], usage(10_100)),
];
messages.push(
  {
    ...msg("inplacecompaction", []),
    llm_data: undefined,
    user_data: JSON.stringify({
      trims: [{ sequence_id: 3, tool_use_id: "t1" }],
      hidden_sequence_ids: [4],
    }),
  } as unknown as Message,
  msg("user", [{ Type: 6, ToolUseID: "t2", ToolResult: [{ Type: 2, Text: "Compacted." }] }]),
  msg("agent", [{ Type: 2, Text: "done" }], usage(150)),
);

const points = contextCompositionPoints(messages);
assert(points.length === 3, `points = ${points.length}`);
const [, before, after] = points;
assert(after.segment !== before.segment, "in-place compaction starts a new segment");
assert(before.segment === points[0].segment, "no compaction, same segment");
const fileRead = (p: (typeof points)[number]) => p.parts["bash:file read"] || 0;
assert(fileRead(before) > 9_000, `file read before = ${fileRead(before)}`);
assert(fileRead(after) < 1_000, `trimmed file read after = ${fileRead(after)}`);
assert(!JSON.stringify(after.toolBreakdown).includes("-"), "no negative breakdown");
const sum = Object.values(after.parts).reduce((a, b) => a + b, 0);
assert(Math.abs(sum - 150) <= 5, `after parts sum to the reported total: ${sum}`);

// Tool-argument tokens are split out per tool category: the first call's
// context holds the bash call's args under bash:file read; nothing else
// carries args there.
assert(before.args["bash:file read"] > 0, `args before = ${JSON.stringify(before.args)}`);
assert(
  Object.values(before.args).reduce((a, b) => a + b, 0) <= before.parts["bash:file read"]!,
  "args never exceed their category",
);
assert(!("text" in before.args) && !("assistant" in before.args), "non-tool categories have no args");

// Reported output/reasoning tokens are exact: thinking takes exactly
// reasoning_tokens of output_tokens, the rest splits by byte ratio. With
// input covering the user message, the segment scale is 1, so the parts
// hold the exact figures.
const exactPoints = contextCompositionPoints([
  msg("user", [{ Type: 2, Text: "go" }]),
  {
    ...msg("agent", [
      { Type: 3, Thinking: "y".repeat(8_000) }, // byte estimate: 2k
      { Type: 2, Text: "done" },
    ]),
    usage_data: JSON.stringify({ input_tokens: 1, output_tokens: 900, reasoning_tokens: 400 }),
  },
]);
const exact = exactPoints[0];
assert(exact.parts["reasoning"] === 400, `reasoning part is the reported figure: ${exact.parts["reasoning"]}`);
assert(exact.parts["assistant"] === 500, `remaining output goes to the text band: ${exact.parts["assistant"]}`);
assert(exact.parts["user"] === 1, `user part untouched: ${exact.parts["user"]}`);

// Without a report, the thinking block's raw byte estimate stands (scale is
// 1: the reported total matches the byte estimates).
const bytePoints = contextCompositionPoints([
  msg("user", [{ Type: 2, Text: "go" }]),
  msg("agent", [{ Type: 3, Thinking: "y".repeat(8_000) }], { usage_data: JSON.stringify({ input_tokens: 1, output_tokens: 2000 }) }),
]);
assert(bytePoints[0].parts["reasoning"] === 2000, `no report, byte estimate stands: ${bytePoints[0].parts["reasoning"]}`);

// Tool-argument bytes belong in the output-side estimate: the remaining
// output (output_tokens - reasoning_tokens) splits by byte ratio across
// text and args. A tiny text block next to a large patch must not inflate
// the args by the text/args estimate mismatch (calibration cancels in the
// comparisons: it scales every part equally).
const factorPoints = contextCompositionPoints([
  msg("user", [{ Type: 2, Text: "go" }]),
  {
    ...msg("agent", [
      { Type: 3, Thinking: "y".repeat(1_200) }, // byte estimate: 300
      { Type: 2, Text: "ok" }, // byte estimate: 1
      { Type: 5, ID: "t1", ToolName: "patch", ToolInput: { patch: "x".repeat(8_000) } }, // byte estimate: ~2005
    ]),
    usage_data: JSON.stringify({ input_tokens: 1, output_tokens: 320, reasoning_tokens: 300 }),
  },
]);
const fp = factorPoints[0];
const sumParts = Object.values(fp.parts).reduce((a, b) => a + b, 0);
assert(
  fp.parts["repo/edit"] < 40,
  `args track the byte ratio of the remaining output: ${JSON.stringify(fp.parts)}`,
);
assert(
  Math.abs(fp.parts["reasoning"] / sumParts - 300 / 321) < 0.001,
  `reasoning holds its share of the context: ${fp.parts["reasoning"]} of ${sumParts}`,
);

if (failed) process.exit(1);
console.log("✓ in-place compaction resets the composition");
