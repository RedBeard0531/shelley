<!-- Live status of a narrated diff review. It covers the viewer header's
     selectors while active, so recording costs no vertical space: the
     microphone level and what is being captured (the pointer/selection
     breadcrumb), then saving and sending. -->
<template>
  <div
    class="review-recording-status"
    :data-phase="phase"
    role="status"
    :aria-live="phase === 'recording' ? 'off' : 'polite'"
    data-testid="review-recording-status"
    data-review-ignore
  >
    <template v-if="phase === 'recording'">
      <span class="recording-status-dot recording" aria-hidden="true" />
      <div class="recording-waveform" aria-hidden="true">
        <span
          v-for="(level, index) in levels"
          :key="index"
          class="recording-waveform-bar"
          :style="{ height: `${Math.max(2, level * 18)}px` }"
        />
      </div>
      <span v-if="current.selection" class="review-recording-kind">Selected</span>
      <span class="review-recording-where" data-testid="review-recording-where" :title="where">
        <span v-for="(crumb, index) in crumbs" :key="index" class="review-recording-crumb">{{
          crumb
        }}</span>
      </span>
    </template>
    <template v-else>
      <span class="recording-status-dot starting" aria-hidden="true" />
      <span class="review-recording-text">{{ phaseText[phase] }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { ReviewCaptureState } from "./reviewCapture";
import type { ReviewRecordingPhase } from "./useReviewRecording";

const props = defineProps<{
  phase: ReviewRecordingPhase;
  levels: number[];
  current: ReviewCaptureState;
}>();

const where = computed(
  () =>
    props.current.selection ||
    props.current.pointer ||
    props.current.screen ||
    props.current.view ||
    "Recording",
);
const crumbs = computed(() => where.value.split(" › "));

const phaseText: Record<ReviewRecordingPhase, string> = {
  idle: "",
  recording: "",
  starting: "Waiting for the microphone…",
  stopping: "Saving…",
  sending: "Sending…",
};
</script>
