<!-- list_subagents: the parent's view of its subagents. Parses the tool's
     "- slug (state): preview" lines; each slug links to its conversation. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">⚡</span>
        <span class="tool-command">list subagents{{ headline ? ` · ${headline}` : "" }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Subagents:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <div
          v-if="hasError || entries.length === 0"
          :class="`tool-code ${hasError ? 'error' : ''}`"
        >
          {{ resultText || "(no output)" }}
        </div>
        <ul v-else class="list-subagents-tool-list">
          <li v-for="entry in entries" :key="entry.slug">
            <a :href="`/c/${encodeURIComponent(entry.slug)}`" @click="open($event, entry.slug)">{{
              entry.slug
            }}</a>
            <span class="list-subagents-tool-state">{{ entry.state }}</span>
            <span v-if="entry.preview" class="list-subagents-tool-preview">{{
              entry.preview
            }}</span>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import { navigateToConversationSlug } from "../../composables/subagentLive";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = ref(false);

const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const entries = computed(() =>
  resultText.value
    .split("\n")
    .map((line) => /^- (\S+) \((working|idle)\)(?:: (.*))?$/.exec(line))
    .filter((m): m is RegExpExecArray => m !== null)
    .map((m) => ({ slug: m[1], state: m[2], preview: m[3] ?? "" })),
);

const headline = computed(() => {
  if (!isComplete.value || props.hasError) return "";
  const working = entries.value.filter((e) => e.state === "working").length;
  const idle = entries.value.length - working;
  if (entries.value.length === 0) return "none";
  return [working && `${working} working`, idle && `${idle} idle`].filter(Boolean).join(", ");
});

function open(event: MouseEvent, slug: string) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0)
    return;
  event.preventDefault();
  navigateToConversationSlug(encodeURIComponent(slug));
}
</script>

<style scoped>
.list-subagents-tool-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: var(--font-size-14);
}
.list-subagents-tool-state {
  margin-left: 0.5rem;
  color: var(--text-secondary);
  font-family: var(--font-mono);
}
.list-subagents-tool-preview {
  display: block;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
}
</style>
