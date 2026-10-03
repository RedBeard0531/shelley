<!-- A message delivered from another conversation (a subagent's message_parent,
     or a parent's message to its subagent), rendered as a tool card: the
     sender's slug, a "message" tag, and the first line. A message that fits on
     one short line shows in full in the header; longer ones expand into the
     message body, rendered like any other message. -->
<template>
  <div class="tool" data-testid="conversation-message-card">
    <div
      :class="['tool-header', { 'tool-header--static': !expandable }]"
      @click="expandable && (isExpanded = !isExpanded)"
    >
      <div class="tool-summary">
        <span class="tool-emoji">⚡</span>
        <span class="tool-command conversation-message-card-slug" @click.stop>
          <ConversationSourceLink :source="source" />
        </span>
        <span class="tool-tag">message</span>
        <span
          :class="['tool-command', { 'conversation-message-card-full': !expandable }]"
          data-testid="conversation-message-card-headline"
          >{{ firstLine }}</span
        >
      </div>
      <button
        v-if="expandable"
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
    <div v-if="isExpanded" class="tool-details">
      <CitedText
        :text="text"
        :markdown-text="text"
        :citations="[]"
        :render-markdown="markdownMode !== 'off'"
        :message-id="messageId"
        :cache-owner="cacheOwner"
        run-key="conversation-message"
        rewrite-localhost-links
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { ConversationMessageSource } from "../../utils/messageSource";
import { useMarkdownMode } from "../composables/markdownMode";
import CitedText from "./CitedText.vue";
import ConversationSourceLink from "./ConversationSourceLink.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{
  source: ConversationMessageSource;
  text: string;
  messageId: string;
  cacheOwner: object;
}>();

/** Longest single-line message shown in full in the header. */
const HEADLINE_MAX = 80;

const { markdownMode } = useMarkdownMode();
const isExpanded = ref(false);
const trimmed = computed(() => props.text.trim());
const firstLine = computed(() => trimmed.value.split(/\r?\n/)[0]);
const expandable = computed(
  () => firstLine.value !== trimmed.value || trimmed.value.length > HEADLINE_MAX,
);
</script>

<style scoped>
.conversation-message-card-slug {
  flex-shrink: 0;
}
.conversation-message-card-slug a {
  color: var(--link-color);
}
/* Nothing to expand: wrap rather than hide the end of the message. */
.conversation-message-card-full {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
