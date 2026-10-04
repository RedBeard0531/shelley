<!-- Drawer badge for expanding a conversation's background jobs inline. -->
<template>
  <button
    type="button"
    class="conversation-background-jobs-badge"
    data-testid="background-jobs-badge"
    :title="label"
    :aria-label="label"
    :aria-expanded="expanded"
    :aria-controls="expanded ? `background-jobs-${conversationId}` : undefined"
    @click.stop="$emit('toggle')"
    @auxclick.stop
  >
    <GearIcon class="conversation-background-jobs-icon" />
    {{ count }}
    <svg
      fill="none"
      stroke="currentColor"
      viewBox="0 0 24 24"
      :class="`drawer-subagent-chevron ${expanded ? 'drawer-subagent-chevron-expanded' : 'drawer-subagent-chevron-collapsed'}`"
      aria-hidden="true"
    >
      <path stroke-linecap="round" stroke-linejoin="round" :stroke-width="2" d="M9 5l7 7-7 7" />
    </svg>
  </button>
</template>

<script setup lang="ts">
import { computed } from "vue";
import GearIcon from "./GearIcon.vue";

const props = defineProps<{ conversationId: string; count: number; expanded: boolean }>();
defineEmits<{ toggle: [] }>();

const label = computed(
  () =>
    `${props.expanded ? "Hide" : "Show"} ${props.count} background job${props.count === 1 ? "" : "s"}`,
);
</script>
