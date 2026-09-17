<template>
  <div class="message-author-conversation" data-testid="message-author-conversation">
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
import type { ConversationMessageSource } from "../../utils/messageSource";
import { ConversationsListKey, navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{ source: ConversationMessageSource }>();
const conversations = inject(ConversationsListKey, null);
// Names arrive asynchronously and can change; identity is always the stable ID.
const slug = computed(() => {
  const sender = conversations?.value.find(
    (c) => c.conversation_id === props.source.conversationId,
  );
  return sender?.slug || props.source.slug || props.source.conversationId;
});

function openSource(event: MouseEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0)
    return;
  event.preventDefault();
  navigateToConversationSlug(encodeURIComponent(props.source.conversationId));
}
</script>
