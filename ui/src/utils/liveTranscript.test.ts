import type { Message } from "../types";
import { delegatedSpeech, formatVoiceContext, liveDialogueMessage } from "./liveTranscript";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

const user = (text: string, startMs: number, endMs: number) =>
  ({ speaker: "user", text, startMs, endMs }) as const;
const live = (text: string, startMs: number, endMs: number) =>
  ({ speaker: "assistant", text, startMs, endMs }) as const;

// delegatedSpeech: the latest continuous request, including words after the
// delegation signal; old small talk is not a task.
assert(
  delegatedSpeech(
    [
      user("Hi there.", 100, 900),
      live("Hello!", 1000, 1500),
      user("Please inspect README", 6000, 6800),
      user(" and summarize it.", 6900, 7800),
    ],
    6800,
  ) === "Please inspect README and summarize it.",
  "late continuation",
);
assert(delegatedSpeech([user("Hi there.", 100, 900)], 11_000) === "", "old small talk");
assert(delegatedSpeech([], 0) === "", "no speech");

// formatVoiceContext: verbatim turns labelled by speaker, newest last.
assert(
  formatVoiceContext([
    user("there's a typo. ", 0, 500),
    user(" in README", 500, 900),
    live("Which file?", 1000, 1500),
    user("yes, that one", 2000, 2500),
  ]) === "USER: there's a typo.  in README\nLIVE: Which file?\nUSER: yes, that one",
  "turns",
);

// formatVoiceContext keeps everything since the previous delegation, so a
// request Live acknowledged without delegating reaches the next one.
{
  const missed = [
    user("Keep the message button on the left.", 0, 900),
    live("Sure, I'll keep that.", 1000, 1500),
    user("Okay.", 2000, 2200),
    live("Anything else?", 2300, 2600),
    user("Hmm.", 3000, 3200),
    live("Take your time.", 3300, 3600),
    user("Can you delegate that?", 4000, 4600),
  ];
  const context = formatVoiceContext([user("Earlier task.", -2000, -1000), ...missed], 1);
  assert(
    context.startsWith("USER: Keep the message button on the left."),
    `missed request kept: ${context}`,
  );
  assert(!context.includes("Earlier task."), "already delegated speech is left out");
  assert(
    formatVoiceContext(missed.slice(-2), 2).split("\n").length === 2,
    "at least the recent turns even when nothing is new",
  );
}

// liveDialogueMessage: typed requests (without their voice exchange) and
// final replies only.
const message = (row: Partial<Message>): Message =>
  ({ sequence_id: 1, conversation_id: "c", message_id: "m", ...row }) as Message;
const text = (t: string) => JSON.stringify({ Content: [{ Type: 2, Text: t }] });
assert(
  liveDialogueMessage(
    message({
      type: "user",
      llm_data: text("Spoken request:\nlint it\n\nVoice exchange (for context only):\nLIVE: Sure."),
    }),
  )?.text === "Spoken request:\nlint it",
  "spoken request without its exchange",
);
assert(
  liveDialogueMessage(
    message({
      type: "user",
      llm_data: text("report"),
      user_data: JSON.stringify({ sender_conversation_id: "x" }),
    }),
  ) === null,
  "subagent reports are not dialogue",
);
assert(
  liveDialogueMessage(
    message({ type: "agent", llm_data: text("working…"), end_of_turn: false }),
  ) === null,
  "progress is not dialogue",
);
assert(
  liveDialogueMessage(message({ type: "agent", llm_data: text("done"), end_of_turn: true }))
    ?.role === "assistant",
  "final reply",
);
console.log("✓ liveTranscript");
