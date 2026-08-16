// Unit tests for collapsedThinkingText — the head/tail 10-line slicing that
// caps collapsed thinking blocks (see utils/thinkingText.ts).
// Run with: tsx src/utils/thinkingText.test.ts

import {
  collapsedThinkingHasMore,
  collapsedThinkingText,
  MAX_THINKING_PREVIEW_LINES,
} from "./thinkingText";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

const N = MAX_THINKING_PREVIEW_LINES;
function lines(n: number): string {
  return Array.from({ length: n }, (_, i) => `line ${i + 1}`).join("\n");
}

// Empty text is empty in both modes.
{
  assert(collapsedThinkingText("", false) === "", "head: empty stays empty");
  assert(collapsedThinkingText("", true) === "", "tail: empty stays empty");
}

// Fewer than the cap: everything is shown, in order.
{
  const short = lines(3);
  assert(collapsedThinkingText(short, false) === short, "head: short text unchanged");
  assert(collapsedThinkingText(short, true) === short, "tail: short text unchanged");
}

// Exactly at the cap is unchanged in both modes.
{
  const exact = lines(N);
  assert(collapsedThinkingText(exact, false) === exact, "head: exactly 10 lines unchanged");
  assert(collapsedThinkingText(exact, true) === exact, "tail: exactly 10 lines unchanged");
}

// More than the cap: head keeps the first N, tail keeps the last N.
{
  const long = lines(15);
  assert(collapsedThinkingText(long, false) === lines(N), "head: keeps the first 10 lines");
  assert(
    collapsedThinkingText(long, true) === lines(15).split("\n").slice(-N).join("\n"),
    "tail: keeps the last 10 lines",
  );
}

// The tail must NOT contain the head when they differ (guards the streaming
// preview showing the newest thoughts, not the opening ones).
{
  const long = lines(15);
  assert(
    collapsedThinkingText(long, true) !== collapsedThinkingText(long, false),
    "tail slice differs from head slice when text overflows",
  );
  assert(
    collapsedThinkingText(long, true).endsWith("line 15"),
    "tail slice ends with the newest line",
  );
  assert(
    collapsedThinkingText(long, true).startsWith(`line ${15 - N + 1}`),
    "tail slice starts at the right line",
  );
}

// Trailing newline does not create phantom lines.
{
  const text = "one\ntwo\n";
  assert(collapsedThinkingText(text, false) === text, "head: trailing newline preserved");
  assert(
    collapsedThinkingText("one\ntwo\nthree", false, 2) === "one\ntwo",
    "custom maxLines honored",
  );
}

// collapsedThinkingHasMore gates the clip-edge fade: false when everything
// fits the cap, true only when lines are actually hidden.
{
  assert(collapsedThinkingHasMore("") === false, "has-more: empty is false");
  assert(collapsedThinkingHasMore(lines(3)) === false, "has-more: 3 lines is false");
  assert(collapsedThinkingHasMore(lines(N)) === false, "has-more: exactly 10 lines is false");
  assert(collapsedThinkingHasMore(lines(11)) === true, "has-more: 11 lines is true");
  assert(collapsedThinkingHasMore("a\nb\nc", 2) === true, "has-more: custom maxLines honored");
  assert(
    collapsedThinkingHasMore("\n".repeat(N - 1)) === false,
    "has-more: N blank lines is false",
  );
}

if (failed > 0) {
  console.error(`✗ ${failed}/${passed + failed} assertions failed`);
  process.exit(1);
}
console.log(`✓ ${passed} assertions passed`);
