<!-- A working subagent in the status bar's running-work popover. Exists to
     give each row its own useSubagentLive subscription. -->
<template>
  <SubagentLiveStrip :slug="conversation.slug ?? ''" :activity="activity" @open="emit('open')" />
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { ConversationWithState } from "../../types";
import { useSubagentLive } from "../composables/subagentLive";
import SubagentLiveStrip from "./SubagentLiveStrip.vue";

const props = defineProps<{ conversation: ConversationWithState }>();
const emit = defineEmits<{ open: [] }>();

const { activity } = useSubagentLive(
  computed(() => props.conversation.slug ?? ""),
  computed(() => props.conversation.conversation_id),
);
</script>
