// Pure helpers for Shelley's Live delegation. GPT-Live emits transcript
// fragments without turn boundaries; these recover the request a delegation
// is about and the exchange it came from, and pick the chat rows Live hears.
import type { LLMContent, Message } from "../types";
import { isTypedUserMessage } from "./conversationView";

const LLM_TYPE_TEXT = 2;
const VOICE_EXCHANGE_SEPARATOR = "\n\nVoice exchange (for context only):\n";

export interface VoiceFragment {
  speaker: "user" | "assistant";
  text: string;
  startMs: number;
  endMs: number;
}

// The user's latest continuous speech (pauses under 5s) ending near the
// delegation, so that an old greeting is not mistaken for a task whose
// transcript has not arrived yet.
export function delegatedSpeech(fragments: VoiceFragment[], offsetMs: number): string {
  const user = fragments.filter((f) => f.speaker === "user");
  const last = user.at(-1);
  if (!last || last.endMs + 2500 < offsetMs) return "";
  let first = user.length - 1;
  while (first > 0 && user[first].startMs - user[first - 1].endMs <= 5000) first--;
  return user
    .slice(first)
    .map((f) => f.text)
    .join("")
    .replace(/\s+/g, " ")
    .trim();
}

// The exchange since the previous delegation (fragments from index since
// on), and at least the last few turns, labelled by speaker with fragments
// kept verbatim. Covering everything since the previous delegation carries a
// request Live acknowledged without delegating into the next delegation.
export function formatVoiceContext(fragments: VoiceFragment[], since = fragments.length): string {
  const turns: { speaker: VoiceFragment["speaker"]; text: string; last: number }[] = [];
  fragments.forEach((fragment, i) => {
    const previous = turns.at(-1);
    if (previous?.speaker === fragment.speaker && i !== since) {
      previous.text += fragment.text;
      previous.last = i;
    } else turns.push({ speaker: fragment.speaker, text: fragment.text, last: i });
  });
  const fresh = turns.findIndex((turn) => turn.last >= since);
  const first = Math.max(0, Math.min(fresh < 0 ? turns.length : fresh, turns.length - 6));
  return turns
    .slice(first)
    .map((turn) => `${turn.speaker === "user" ? "USER" : "LIVE"}: ${turn.text.trim()}`)
    .join("\n")
    .slice(-5000);
}

export function messageText(message: Message): string {
  if (!message.llm_data) return "";
  try {
    const llm =
      typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    const content: LLMContent[] = llm?.Content || [];
    return content
      .filter((c) => c.Type === LLM_TYPE_TEXT && c.Text)
      .map((c) => c.Text)
      .join("\n")
      .trim();
  } catch {
    return "";
  }
}

// Live hears only the dialogue the user has with the backend: typed (or
// spoken) requests and final replies, not tool results, subagent reports,
// background jobs or progress.
export function liveDialogueMessage(
  message: Message,
): { role: "user" | "assistant"; text: string } | null {
  const text = messageText(message);
  if (!text) return null;
  if (message.type === "user") {
    // The exchange stays visible in chat and to the backend, but replaying
    // it here would attribute Live's own speech to the user.
    const task = text.split(VOICE_EXCHANGE_SEPARATOR, 1)[0].trim();
    return isTypedUserMessage(message) && task ? { role: "user", text: task } : null;
  }
  return message.type === "agent" && message.end_of_turn ? { role: "assistant", text } : null;
}
