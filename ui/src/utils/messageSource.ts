export interface ConversationMessageSource {
  conversationId: string;
  slug: string;
  relationship: "subagent" | "parent";
}

export interface BackgroundJobOutcome {
  command: string;
  // Null when the job was lost: the host rebooted or the job was killed
  // before it could record its exit status.
  exitCode: number | null;
  // Go duration string, empty when the job was lost.
  duration: string;
  logPath: string;
  tail: string;
}

export interface BackgroundJobMessageSource {
  backgroundJobId: string;
  // Absent on notices recorded before outcomes were stored structurally.
  outcome?: BackgroundJobOutcome;
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
  if (typeof backgroundJobId === "string" && backgroundJobId) {
    const outcome = backgroundJobOutcome(parsed);
    return outcome ? { backgroundJobId, outcome } : { backgroundJobId };
  }

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

function backgroundJobOutcome(parsed: Record<string, unknown>): BackgroundJobOutcome | null {
  const { command, exit_code: exitCode, duration, log_path: logPath, tail } = parsed;
  if (
    typeof command !== "string" ||
    !command ||
    typeof duration !== "string" ||
    typeof logPath !== "string" ||
    typeof tail !== "string"
  ) {
    return null;
  }
  return {
    command,
    exitCode: typeof exitCode === "number" ? exitCode : null,
    duration,
    logPath,
    tail,
  };
}
