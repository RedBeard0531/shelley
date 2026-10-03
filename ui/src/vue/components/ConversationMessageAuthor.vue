<template>
  <div
    v-if="'backgroundJobId' in source"
    class="message-author-conversation"
    data-testid="message-author-background-job"
  >
    Background job {{ source.backgroundJobId }}
  </div>
  <div v-else class="message-author-conversation" data-testid="message-author-conversation">
    Message from
    <a
      :href="`/c/${encodeURIComponent(source.conversationId)}`"
      :title="
        source.relationship === 'parent' ? 'Open parent conversation' : 'Open subagent conversation'
      "
      @click="openSource"
      >{{ slug }}</a
    >
  </div>
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import type { MessageSource } from "../../utils/messageSource";
import { ConversationsListKey, navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{ source: MessageSource }>();
const conversations = inject(ConversationsListKey, null);
// Names arrive asynchronously and can change; identity is always the stable ID.
const slug = computed(() => {
  const source = props.source;
  if ("backgroundJobId" in source) return "";
  const sender = conversations?.value.find((c) => c.conversation_id === source.conversationId);
  return sender?.slug || source.slug || source.conversationId;
});

function openSource(event: MouseEvent) {
  const source = props.source;
  if ("backgroundJobId" in source) return;
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0)
    return;
  event.preventDefault();
  navigateToConversationSlug(encodeURIComponent(source.conversationId));
}
</script>
