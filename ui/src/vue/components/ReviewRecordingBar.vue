<!-- Notices about narrated diff reviews: an error and any unsent
     recordings, with Send/Download/Discard. It stays put while a recording
     is live (the live status is ReviewRecordingStatus, in the header) so
     the content below does not jump. -->
<template>
  <div
    v-if="error || pending.length"
    class="review-recording-bar"
    role="status"
    data-testid="review-recording-bar"
    data-review-ignore
  >
    <span
      v-if="error"
      class="review-recording-error"
      role="alert"
      data-testid="review-recording-error"
      >{{ error }}</span
    >
    <div
      v-for="session in pending"
      :key="session.id"
      class="review-recording-pending"
      data-testid="review-recording-pending"
    >
      <span
        >Unsent recording · {{ formatDuration(session.durationMs) }} · {{ startedLabel(session)
        }}{{
          conversationId && session.conversationId !== conversationId
            ? " · from another conversation"
            : ""
        }}</span
      >
      <button
        type="button"
        class="btn btn-primary"
        data-testid="review-recording-send"
        :disabled="busy"
        @click="emit('retry', session)"
      >
        Send
      </button>
      <button type="button" class="btn btn-secondary" @click="emit('download', session)">
        Download
      </button>
      <button
        type="button"
        class="btn btn-secondary"
        data-testid="review-recording-discard"
        :disabled="busy"
        @click="confirmDiscard(session)"
      >
        {{ discarding === session.id ? "Discard for good?" : "Discard" }}
      </button>
    </div>
    <button
      v-if="error && !pending.length"
      type="button"
      class="btn btn-secondary"
      @click="emit('dismiss')"
    >
      Dismiss
    </button>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import type { ReviewSession } from "./reviewRecordingStore";
import { formatDuration } from "./reviewRecordingFormat";

defineProps<{
  // A recording is live or being sent; unsent ones wait their turn.
  busy: boolean;
  error: string;
  pending: ReviewSession[];
  // The viewer's conversation; unsent recordings still go to their own.
  conversationId?: string;
}>();
const emit = defineEmits<{
  (e: "retry", session: ReviewSession): void;
  (e: "download", session: ReviewSession): void;
  (e: "discard", session: ReviewSession): void;
  (e: "dismiss"): void;
}>();

const discarding = ref<string | null>(null);
let discardTimer: number | null = null;
onBeforeUnmount(() => {
  if (discardTimer !== null) window.clearTimeout(discardTimer);
});
function confirmDiscard(session: ReviewSession) {
  if (discardTimer !== null) window.clearTimeout(discardTimer);
  if (discarding.value === session.id) {
    discarding.value = null;
    emit("discard", session);
    return;
  }
  discarding.value = session.id;
  discardTimer = window.setTimeout(() => (discarding.value = null), 4000);
}

function startedLabel(session: ReviewSession): string {
  return new Date(session.startedAt).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}
</script>
