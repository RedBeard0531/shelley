import assert from "node:assert/strict";
import { conversationMessageSource } from "./messageSource";

for (const relationship of ["subagent", "parent"] as const) {
  const data = {
    sender_conversation_id: "sender-conversation",
    sender_slug: relationship === "parent" ? "implement-api" : "backend",
    sender_relationship: relationship,
    Text: "progress",
  };
  const expected = {
    conversationId: data.sender_conversation_id,
    slug: data.sender_slug,
    relationship,
  };
  assert.deepEqual(conversationMessageSource(data), expected);
  assert.deepEqual(conversationMessageSource(JSON.stringify(data)), expected);
}

assert.deepEqual(
  conversationMessageSource({
    sender_conversation_id: "unnamed-parent",
    sender_slug: "",
    sender_relationship: "parent",
  }),
  { conversationId: "unnamed-parent", slug: "", relationship: "parent" },
);

for (const data of [
  null,
  "not json",
  [],
  { sender_slug: "missing-id", sender_relationship: "parent" },
  { sender_conversation_id: "missing-slug", sender_relationship: "subagent" },
  { sender_conversation_id: "id", sender_slug: "slug" },
  { sender_conversation_id: "id", sender_slug: "slug", sender_relationship: "user" },
]) {
  assert.equal(conversationMessageSource(data), null);
}
