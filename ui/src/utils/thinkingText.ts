// Pure line-slicing for the collapsed thinking preview. Separated from
// ThinkingContent.vue so the user-facing "10 lines" cap is pinned by unit
// tests: head (first maxLines newline-lines, finalized messages) or tail
// (last maxLines newline-lines, live stream). The CSS clamps in styles.css
// remain the authoritative bound for lines that wrap past one visual row.

/** Cap on rendered lines while collapsed. Mirrored in styles.css. */
export const MAX_THINKING_PREVIEW_LINES = 10;

export function collapsedThinkingText(
  text: string,
  showTail: boolean,
  maxLines: number = MAX_THINKING_PREVIEW_LINES,
): string {
  if (!text) return "";
  const lines = text.split("\n");
  const slice = showTail ? lines.slice(-maxLines) : lines.slice(0, maxLines);
  return slice.join("\n");
}

/**
 * Does the collapsed view hide any lines beyond the cap? Used to show a fade
 * at the clip edge only when there is genuinely more to reveal. (Lines that
 * wrap past the cap without exceeding maxLines are detected visually by the
 * component via scrollHeight vs clientHeight.)
 */
export function collapsedThinkingHasMore(
  text: string,
  maxLines: number = MAX_THINKING_PREVIEW_LINES,
): boolean {
  return text.split("\n").length > maxLines;
}
