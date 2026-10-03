import assert from "node:assert/strict";
import { messageSource } from "./messageSource";

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
  assert.deepEqual(messageSource(data), expected);
  assert.deepEqual(messageSource(JSON.stringify(data)), expected);
}

assert.deepEqual(
  messageSource({
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
  assert.equal(messageSource(data), null);
}

for (const data of [
  { background_job_id: "1a2b3c4d", Text: "Background job 1a2b3c4d finished: exit 0, 2m3s." },
  '{"background_job_id":"1a2b3c4d","Text":"done"}',
]) {
  assert.deepEqual(messageSource(data), { backgroundJobId: "1a2b3c4d" });
}
assert.equal(messageSource({ background_job_id: "" }), null);
