export interface ConversationMessageSource {
  conversationId: string;
  slug: string;
  relationship: "subagent" | "parent";
}

export interface BackgroundJobMessageSource {
  backgroundJobId: string;
}

export type MessageSource = ConversationMessageSource | BackgroundJobMessageSource;

// Persisted messages carry JSON text; queued ghosts carry the decoded object.
function parseUserData(userData: unknown): Record<string, unknown> | null {
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
  return parsed as Record<string, unknown>;
}

// messageSource identifies who, other than a human, sent a user message:
// another conversation, or a backgrounded bash job that finished.
export function messageSource(userData: unknown): MessageSource | null {
  const parsed = parseUserData(userData);
  if (!parsed) return null;

  const { background_job_id: backgroundJobId } = parsed;
  if (typeof backgroundJobId === "string" && backgroundJobId) return { backgroundJobId };

  const {
    sender_conversation_id: conversationId,
    sender_slug: slug,
    sender_relationship: relationship,
  } = parsed;
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
