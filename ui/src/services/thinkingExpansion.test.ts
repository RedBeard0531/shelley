// Unit tests for thinkingExpansion.ts — the shared collapsed/expanded state
// for thinking blocks and the streaming → finalized handoff.
// Run with: tsx src/services/thinkingExpansion.test.ts

import {
  getThinkingExpanded,
  setThinkingExpanded,
  handoffStreamingThinking,
  clearStreamingThinkingExpansion,
  STREAMING_THINKING_KEY,
} from "./thinkingExpansion";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// Raw stream messages carry the LLM payload as a JSON string in llm_data.
function msg(
  messageId: string,
  type: string,
  content: Array<{ Type: number; Thinking?: string; Text?: string }>,
) {
  return { message_id: messageId, type, llm_data: JSON.stringify({ Content: content }) };
}

// Default state is collapsed.
{
  assert(getThinkingExpanded("msg:1") === false, "unknown key starts collapsed");
}

// Set / get round trip; setting false forgets the entry.
{
  setThinkingExpanded("msg:1", true);
  assert(getThinkingExpanded("msg:1") === true, "expanded after set(true)");
  setThinkingExpanded("msg:1", false);
  assert(getThinkingExpanded("msg:1") === false, "collapsed after set(false)");
}

// Handoff moves the live expansion onto the newest agent message with a
// thinking block, and clears the live key so the next turn starts collapsed.
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  handoffStreamingThinking([
    msg("older", "agent", [{ Type: 3, Thinking: "older thought" }]),
    msg("newest", "agent", [{ Type: 3, Thinking: "newest thought" }]),
  ]);
  assert(getThinkingExpanded("newest") === true, "handoff expanded the newest thinking message");
  assert(getThinkingExpanded("older") === false, "handoff left older messages collapsed");
  assert(getThinkingExpanded(STREAMING_THINKING_KEY) === false, "live key cleared after handoff");
}

// Handoff with no agent thinking block just clears the live key (the streamed
// thinking vanished, e.g. it was refused or this message has no thinking).
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  handoffStreamingThinking([msg("m", "agent", [{ Type: 2, Text: "no thinking here" }])]);
  assert(getThinkingExpanded("m") === false, "no expansion written without a thinking block");
  assert(
    getThinkingExpanded(STREAMING_THINKING_KEY) === false,
    "live key cleared when nothing to hand off",
  );
}

// Handoff ignores user messages, non-thinking content, and malformed llm_data.
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  handoffStreamingThinking([
    msg("user-1", "user", [{ Type: 2, Text: "hi" }]),
    msg("agent-1", "agent", [{ Type: 4, Thinking: "" }]), // redacted_thinking
    msg("agent-2", "agent", [{ Type: 4, Thinking: "" }]),
    { message_id: "agent-3", type: "agent", llm_data: "{not json" }, // malformed
  ]);
  assert(getThinkingExpanded("agent-1") === false, "redacted thinking is not expanded");
  assert(getThinkingExpanded("agent-2") === false, "redacted thinking is not expanded");
  assert(getThinkingExpanded("agent-3") === false, "malformed llm_data is not expanded");
  assert(getThinkingExpanded(STREAMING_THINKING_KEY) === false, "live key cleared");
}

// Handoff targets strictly the newest agent message: an older re-broadcast
// message with thinking must not catch an expansion meant for the current
// (thinking-less) message.
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  handoffStreamingThinking([
    msg("older-with-thinking", "agent", [{ Type: 3, Thinking: "old thought" }]),
    msg("newest-no-thinking", "agent", [{ Type: 2, Text: "final answer" }]),
  ]);
  assert(
    getThinkingExpanded("older-with-thinking") === false,
    "older thinking message is not opened by the handoff",
  );
  assert(
    getThinkingExpanded(STREAMING_THINKING_KEY) === false,
    "live key cleared when the newest message has no thinking",
  );
}

// Handoff also understands an already-parsed object llm_data (mirrors the raw
// string path used by the stream).
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  handoffStreamingThinking([
    {
      message_id: "obj-1",
      type: "agent",
      llm_data: { Content: [{ Type: 3, Thinking: "thought" }] },
    },
  ]);
  assert(getThinkingExpanded("obj-1") === true, "object llm_data hands off expansion");
}

// Handoff is a no-op when the live block was never expanded.
{
  handoffStreamingThinking([msg("m", "agent", [{ Type: 3, Thinking: "thought" }])]);
  assert(getThinkingExpanded("m") === false, "collapsed stream hands off collapsed");
}

// clearStreamingThinkingExpansion forgets the live entry.
{
  setThinkingExpanded(STREAMING_THINKING_KEY, true);
  clearStreamingThinkingExpansion();
  assert(getThinkingExpanded(STREAMING_THINKING_KEY) === false, "live key cleared explicitly");
}

if (failed > 0) {
  console.error(`✗ ${failed}/${passed + failed} assertions failed`);
  process.exit(1);
}
console.log(`✓ ${passed} assertions passed`);
