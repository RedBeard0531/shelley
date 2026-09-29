<!-- Record/stop control for narrated diff reviews (useReviewRecording). -->
<template>
  <button
    v-if="phase === 'recording'"
    v-tooltip.top="'Stop and send to the conversation'"
    type="button"
    class="review-record-btn review-record-btn-stop"
    aria-label="Stop recording"
    data-testid="review-record-stop"
    data-review-ignore
    @click="emit('stop')"
  >
    <svg fill="currentColor" viewBox="0 0 24 24" width="10" height="10" aria-hidden="true">
      <rect x="4" y="4" width="16" height="16" rx="2" />
    </svg>
    <time>{{ formatDuration(elapsedMs) }}</time>
  </button>
  <button
    v-else
    v-tooltip.top="'Record a narrated review'"
    type="button"
    class="review-record-btn"
    :disabled="phase !== 'idle'"
    :aria-busy="phase !== 'idle'"
    aria-label="Record review"
    data-testid="review-record-start"
    @click="emit('start')"
  >
    <svg
      fill="none"
      stroke="currentColor"
      stroke-width="1.8"
      viewBox="0 0 24 24"
      width="18"
      height="18"
      aria-hidden="true"
    >
      <rect x="3" y="5" width="13" height="14" rx="2" />
      <path stroke-linecap="round" stroke-linejoin="round" d="m16 10 5-3v10l-5-3z" />
    </svg>
  </button>
</template>

<script setup lang="ts">
import type { ReviewRecordingPhase } from "./useReviewRecording";
import { formatDuration } from "./reviewRecordingFormat";

defineProps<{ phase: ReviewRecordingPhase; elapsedMs: number }>();
const emit = defineEmits<{ (e: "start"): void; (e: "stop"): void }>();
</script>
