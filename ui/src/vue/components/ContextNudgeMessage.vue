<!-- Sub-component of Message.vue. Renders a context-size nudge sent to the
     agent while auto compaction is on (see recordContextNudge and the
     contextNudge predicate in types.ts): user-role so the agent reads it, but
     not typed by the user, so it gets the cwd-change status treatment. -->
<template>
  <div class="message message-gitinfo msg-cwdchange-container" data-testid="message-context-nudge">
    <span class="msg-cwdchange-icon" aria-hidden="true">📏</span>
    <span class="msg-cwdchange-text">{{ text }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";
import { contextNudge } from "../../types";

const props = defineProps<{ message: MessageType }>();

const text = computed(() => contextNudge(props.message) ?? "");
</script>
