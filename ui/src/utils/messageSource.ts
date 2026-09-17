export interface ConversationMessageSource {
  conversationId: string;
  slug: string;
  relationship: "subagent" | "parent";
}

// Persisted messages carry JSON text; queued ghosts carry the decoded object.
export function conversationMessageSource(userData: unknown): ConversationMessageSource | null {
  if (!userData) return null;
  let parsed: unknown = userData;
  if (typeof parsed === "string") {
    try {
      parsed = JSON.parse(parsed);
    } catch {
      return null;
    }
  }
  if (typeof parsed !== "object" || parsed === null) return null;

  const {
    sender_conversation_id: conversationId,
    sender_slug: slug,
    sender_relationship: relationship,
  } = parsed as {
    sender_conversation_id?: unknown;
    sender_slug?: unknown;
    sender_relationship?: unknown;
  };
  if (
    typeof conversationId !== "string" ||
    !conversationId ||
    typeof slug !== "string" ||
    (relationship !== "subagent" && relationship !== "parent")
  ) {
    return null;
  }
  return { conversationId, slug, relationship };
}
