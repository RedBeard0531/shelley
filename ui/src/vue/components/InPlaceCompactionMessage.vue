<!-- Sub-component of Message.vue. Renders an "inplacecompaction" record: earlier
     messages the LLM now sees squished into a summary or with trimmed tool
     results. The original messages still render above it unchanged. -->
<template>
  <div
    class="message message-gitinfo msg-compaction-container"
    data-testid="message-inplacecompaction"
    role="status"
  >
    <div v-for="s in data.squishes ?? []" :key="`s${s.from_sequence_id}`" class="msg-compaction-op">
      <span class="msg-compaction-icon">🗜️</span>
      Compacted messages
      <span class="msg-compaction-range">#{{ s.from_sequence_id }}–#{{ s.to_sequence_id }}</span>
      <details class="msg-compaction-summary">
        <summary>summary</summary>
        <div class="msg-compaction-summary-text">{{ s.summary }}</div>
      </details>
    </div>
    <div v-if="data.trims?.length" class="msg-compaction-op">
      <span class="msg-compaction-icon">✂️</span>
      Trimmed {{ data.trims.length === 1 ? "tool output" : `${data.trims.length} tool outputs` }}
      <span class="msg-compaction-range">{{
        data.trims.map((t) => `#${t.sequence_id}`).join(", ")
      }}</span>
    </div>
    <div v-if="hidden" class="msg-compaction-op">
      <span class="msg-compaction-icon">🙈</span>
      Hid {{ hidden }} from the agent
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { InPlaceCompaction } from "../../generated-types";
import type { Message as MessageType } from "../../types";

const props = defineProps<{ message: MessageType }>();

const data = computed<InPlaceCompaction>(() => {
  const raw = props.message.user_data;
  if (!raw) return {};
  return typeof raw === "string" ? JSON.parse(raw) : (raw as InPlaceCompaction);
});

// The record hides its own traces: context nudges and earlier compaction calls.
const hidden = computed(() => {
  const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
  const parts: string[] = [];
  const nudges = data.value.hidden_sequence_ids?.length ?? 0;
  const calls = data.value.hidden_tool_use_ids?.length ?? 0;
  if (nudges) parts.push(plural(nudges, "context nudge"));
  if (calls) parts.push(plural(calls, "compaction call"));
  return parts.join(" and ");
});
</script>
