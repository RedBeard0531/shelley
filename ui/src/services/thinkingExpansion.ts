// In-memory collapsed/expanded state for thinking blocks, keyed so the state
// survives remounts. The live streaming preview uses STREAMING_THINKING_KEY;
// when a streamed agent message finalizes, handoffStreamingThinking() moves
// that state onto the persisted message's block (keyed by message_id). That
// keeps the preview → finalized-message swap from ever collapsing a block the
// user opened — and, since a collapsed block is capped at the same 10 lines
// in both phases, never shifts the conversation's scroll position under the
// user. State is intentionally session-only: a reload starts every block
// collapsed again.
//
// This module is deliberately framework-free (a plain Map): the components
// that consume it seed their local refs from it on mount, so it can run in
// the Node-based UI test suite without a DOM.
export const STREAMING_THINKING_KEY = "streaming";

/** Structural subset of a raw stream Message the handoff actually reads. */
interface ThinkingMessage {
  message_id: string;
  type: string;
  llm_data?:
    | string
    | { Content?: Array<{ Type: number; Thinking?: string; Text?: string }> }
    | null;
}

const expanded = new Map<string, boolean>();

export function getThinkingExpanded(key: string): boolean {
  return expanded.get(key) ?? false;
}

export function setThinkingExpanded(key: string, value: boolean): void {
  if (value) expanded.set(key, true);
  else expanded.delete(key);
}

/** Forget the live-streaming entry (new turn, conversation switch, finalize). */
export function clearStreamingThinkingExpansion(): void {
  expanded.delete(STREAMING_THINKING_KEY);
}

/** Does the raw message row carry a thinking block? (llm_data is a JSON string on the wire.) */
function hasThinking(m: ThinkingMessage): boolean {
  try {
    const raw = m.llm_data;
    let llmData: { Content?: Array<{ Type: number; Thinking?: string; Text?: string }> } | null =
      null;
    if (typeof raw === "string") {
      llmData = JSON.parse(raw) as {
        Content?: Array<{ Type: number; Thinking?: string; Text?: string }>;
      };
    } else if (raw && typeof raw === "object") {
      llmData = raw;
    }
    // ContentTypeThinking = 3.
    return !!llmData?.Content?.some((c) => c.Type === 3 && (c.Thinking || c.Text));
  } catch {
    return false;
  }
}

/**
 * Called by the stream handler when a finalized agent message arrives. If the
 * live streaming thinking was expanded, carry that expansion to the newest
 * agent message (the one the preview is, by construction, the stand-in for) —
 * only when it actually carries a thinking block — then forget the live entry
 * so the next turn starts collapsed again. Handing off to "any agent message
 * with thinking" could pop open a stale older block when the current
 * message's thinking vanished at finalize.
 */
export function handoffStreamingThinking(messages: ThinkingMessage[]): void {
  if (!expanded.has(STREAMING_THINKING_KEY)) return;
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.type !== "agent") continue;
    // Messages carry at most one thinking block in practice, and keying on the
    // message alone keeps the handoff unambiguous even though thinking blocks
    // have no content ID of their own.
    if (hasThinking(m)) expanded.set(m.message_id, true);
    break;
  }
  expanded.delete(STREAMING_THINKING_KEY);
}
