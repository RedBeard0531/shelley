import { buildCompositionPoints, type CompositionPoint } from "./contextComposition";
import type { Message } from "../types";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}
function run(name: string, fn: () => void) {
  try {
    fn();
  } catch (e) {
    failed++;
    console.error(`FAIL: ${name} threw ${e}`);
  }
}

// llm.go ContentType values
const TEXT = 2;
const THINKING = 3;
const TOOL_USE = 5;
const TOOL_RESULT = 6;

let seq = 0;
function msg(
  type: Message["type"],
  generation: number,
  content: unknown[],
  usage?: Record<string, number>,
  model?: string,
): Message {
  return {
    message_id: `m${++seq}`,
    conversation_id: "c1",
    sequence_id: seq,
    type,
    llm_data: JSON.stringify({ Role: type === "agent" ? 1 : 0, Content: content }),
    usage_data: usage ? JSON.stringify(usage) : null,
    created_at: "",
    generation,
    model_name: model ?? null,
  } as unknown as Message;
}

const text = (t: string) => ({ Type: TEXT, Text: t });
const thinking = (t: string, signature?: string) => ({
  Type: THINKING,
  Thinking: t,
  ...(signature ? { Signature: signature } : {}),
});
const toolUse = (id: string, name: string, input: string) => ({
  Type: TOOL_USE,
  ID: id,
  ToolName: name,
  ToolInput: input,
});
const toolResult = (toolUseId: string, output: string) => ({
  Type: TOOL_RESULT,
  ToolUseID: toolUseId,
  ToolResult: [text(output)],
});
// usage where the prompt is the sum of the parts the test cares about
const usage = (prompt: number, output: number, reasoning = 0) => ({
  input_tokens: prompt,
  cache_creation_input_tokens: 0,
  cache_read_input_tokens: 0,
  output_tokens: output,
  reasoning_tokens: reasoning,
});
const sum = (parts: CompositionPoint["parts"]) =>
  Object.values(parts).reduce((s, v) => s + v, 0);

run("first call: fold covers everything not estimated at chars/4, bands sum to reported total", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("abcd".repeat(100))]), // 400 bytes = 100 chars/4
    msg("user", 1, [text("abcd".repeat(50))]), // 50 tokens at chars/4
    msg("agent", 1, [text("hello")], usage(1000, 10)),
  ]);
  assert(points.length === 1, "one point");
  const p = points[0];
  assert(p.total === 1010, "total = prompt + output");
  assert(p.parts.system === 100 + (1000 - 50 - 100), "system band = chars/4 + fold (tools+framing)");
  assert(p.parts.user === 50, "user content at chars/4 before any rate is known");
  assert(p.parts.assistant === 10, "provisional newest-assistant attribution");
  assert(sum(p.parts) === p.total, "bands sum to the reported total");
});

run("second call: assistant enters at exact output size, user content at exact delta remainder", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("x".repeat(400))]),
    msg("user", 1, [text("y".repeat(400))]),
    msg("agent", 1, [text("z".repeat(400))], usage(1000, 100)),
    msg("user", 1, [text("w".repeat(400))]),
    msg("agent", 1, [text("done")], usage(1500, 50)),
  ]);
  assert(points.length === 2, "two points");
  const [p1, p2] = points;
  assert(p1.parts.assistant === 100, "point 1 provisional assistant");
  // point 2: prompt delta 500 = assistant (100, exact) + user content (400, exact)
  assert(p2.parts.assistant === 100 + 50, "assistant entered at exact output size");
  assert(p2.parts.user === 100 + 400, "user content attributed the exact delta remainder");
  assert(sum(p2.parts) === p2.total, "bands sum to the reported total");
  assert(p2.parts.system === p1.parts.system, "system band unchanged");
});

run("tool args and results land on the tool band, args split by bytes", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("s".repeat(400))]),
    msg("user", 1, [text("u".repeat(400))]),
    // assistant: 400 bytes text + ~404 bytes args; output 100 tokens
    msg("agent", 1, [text("a".repeat(400)), toolUse("t1", "bash", "b".repeat(400))], usage(1000, 100)),
    msg("user", 1, [toolResult("t1", "r".repeat(400))]),
    msg("agent", 1, [text("done")], usage(1500, 20)),
  ]);
  const p2 = points[1];
  assert(p2.args["bash:other"] === 50, `args get their byte share of the exact output (got ${p2.args["bash:other"]})`);
  assert(p2.parts["bash:other"] === 450, `args + tool result on the tool band (got ${p2.parts["bash:other"]})`);
  assert(p2.parts.assistant === 50 + 20, "text keeps the rest");
});

run("reported reasoning tokens pin the reasoning band exactly", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("s".repeat(400))]),
    msg("user", 1, [text("u".repeat(400))]),
    msg("agent", 1, [text("a".repeat(400)), thinking("t".repeat(400))], usage(1000, 100, 60)),
    msg("agent", 1, [text("next")], usage(1100, 20)),
  ]);
  const p2 = points[1];
  assert(p2.parts.reasoning === 60, `reasoning = reported tokens (got ${p2.parts.reasoning})`);
  assert(p2.parts.assistant === 40 + 20, "text keeps the remainder plus its own provisional output");
  assert(sum(p2.parts) === p2.total, "bands sum to the reported total");
});

run("signed thinking: newest turn only, and the strip is corrected out of the delta", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("s".repeat(400))]),
    msg("user", 1, [text("u1".repeat(200))]),
    msg("agent", 1, [thinking("k".repeat(400), "sig"), text("a".repeat(400))], usage(1000, 100, 40)),
    msg("user", 1, [text("r".repeat(400))]),
    // turn 1's signed thinking is still the newest turn at call 2 and only
    // leaves at call 3: 1000 + 100 (turn 1 output) + 400 (user) = 1500
    msg("agent", 1, [thinking("k2".repeat(400), "sig"), text("a2".repeat(400))], usage(1500, 100, 30)),
    msg("agent", 1, [text("next")], usage(1560, 20)),
  ]);
  const [p1, p2, p3] = points;
  assert(p1.parts.reasoning === 40, "provisional reasoning for the newest turn");
  assert(p2.parts.reasoning === 40 + 30, "newest signed turn's thinking in context, plus the current output");
  assert(!("signedThinking" in p2.parts), "signed-thinking bucket merges into the reasoning band");
  assert(p2.parts.user === 100 + 400, "user content at exact delta remainder");
  assert(sum(p2.parts) === p2.total, "bands sum to the reported total");
  assert(p3.parts.reasoning === 30, "only the newest signed turn's thinking remains");
  assert(sum(p3.parts) === p3.total, "bands still sum to the reported total after the strip");
});

run("compaction: reconstruction resets and the system prompt is re-folded", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("s".repeat(400))]),
    msg("user", 1, [text("u".repeat(400))]),
    msg("agent", 1, [text("a".repeat(400))], usage(1000, 100)),
    msg("system", 2, [text("s".repeat(400))]),
    msg("user", 2, [text("summary".repeat(100))]),
    msg("agent", 2, [text("b".repeat(400))], usage(700, 100)),
  ]);
  const p2 = points[1];
  assert(p2.generation === 2, "second point is generation 2");
  assert(p2.parts.user === 175, `only generation-2 content remains (got ${p2.parts.user})`);
  assert(p2.parts.assistant === 100, "provisional output");
  assert(p2.parts.system === 100 + 425, "system re-folded for the new generation");
  assert(sum(p2.parts) === p2.total, "bands sum to the reported total");
});

run("missing usage: content rides into the next step and nothing crashes", () => {
  const points = buildCompositionPoints([
    msg("system", 1, [text("s".repeat(400))]),
    msg("user", 1, [text("u".repeat(400))]),
    msg("agent", 1, [text("a".repeat(400))], usage(1000, 100)),
    msg("agent", 1, [text("lost".repeat(100))]),
    msg("agent", 1, [text("b")], usage(1200, 50)),
  ]);
  assert(points.length === 2, "usage-less call is not a point");
  const p2 = points[1];
  assert(sum(p2.parts) === p2.total, "bands still sum to the reported total");
});

if (failed > 0) {
  console.error(`\n${failed} failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\ncontextComposition tests passed (${passed})`);
