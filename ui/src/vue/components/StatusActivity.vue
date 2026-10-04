<!-- Status-bar summary of work still running under this conversation:
     backgrounded bash jobs and working subagents. Either keeps going after the
     agent's own turn ends, so "Ready" alone undersells what the conversation
     is doing. Counts come from the live conversation list, the same source as
     the drawer's badges. Opens a popover listing both, where a job can be
     stopped and a subagent opened. Renders nothing when nothing is running.
     The popover borrows the context-usage popup's surface: same anchor (a
     status-bar readout segment), same open-upward placement. -->
<template>
  <template v-if="running">
    <button
      ref="triggerRef"
      type="button"
      class="status-activity-button status-readout-control"
      data-testid="status-activity"
      v-tooltip.top="tooltip"
      :aria-label="tooltip"
      aria-haspopup="dialog"
      :aria-expanded="open"
      @click="togglePopup"
    >
      <span v-if="jobCount > 0" class="status-activity-segment" data-testid="status-activity-jobs">
        <GearIcon class="status-activity-icon" />
        <span class="status-readout-affordance">{{ jobCount }}</span>
      </span>
      <span
        v-if="subagents.length > 0"
        class="status-activity-segment"
        data-testid="status-activity-subagents"
      >
        <span class="working-indicator" aria-hidden="true" />
        <span class="status-readout-affordance">{{ subagents.length }}</span>
      </span>
    </button>
    <Popover
      ref="popoverRef"
      :pt="{
        root: {
          class: 'chat-context-popup status-activity-popup',
          'aria-label': 'Running work',
          style: { '--context-popup-available-height': popupAvailableHeight },
        },
        content: { class: 'chat-context-popup-content status-activity-popup-content' },
      }"
      @show="open = true"
      @hide="open = false"
    >
      <!-- [autofocus] is where PrimeVue puts focus on open, so Tab continues
           into the list rather than back out to the status bar. -->
      <section v-if="jobCount > 0">
        <div class="status-activity-heading" tabindex="-1" autofocus>Background jobs</div>
        <BackgroundJobsList :conversation-id="conversationId" :count="jobCount" nested />
      </section>
      <section v-if="subagents.length > 0">
        <div class="status-activity-heading" tabindex="-1" autofocus>Running subagents</div>
        <StatusActivitySubagent
          v-for="sub in subagents"
          :key="sub.conversation_id"
          :conversation="sub"
          @open="popoverRef?.hide()"
        />
      </section>
    </Popover>
  </template>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from "vue";
import Popover from "primevue/popover";
import { ConversationsListKey } from "../composables/subagentLive";
import BackgroundJobsList from "./BackgroundJobsList.vue";
import GearIcon from "./GearIcon.vue";
import StatusActivitySubagent from "./StatusActivitySubagent.vue";

const props = defineProps<{ conversationId: string }>();

const conversations = inject(ConversationsListKey);
if (!conversations) throw new Error("StatusActivity requires ConversationsListKey");

const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const triggerRef = ref<HTMLButtonElement | null>(null);
const open = ref(false);
// Caps the popup at the room above the trigger, as ContextUsageBar does for
// the same surface: job output tails make it tall, and on a short window it
// would otherwise run off the top and cover the trigger.
const popupAvailableHeight = ref<string>();

function togglePopup(event: Event) {
  if (triggerRef.value) {
    popupAvailableHeight.value = `${Math.max(0, triggerRef.value.getBoundingClientRect().top - 16)}px`;
  }
  popoverRef.value?.toggle(event);
}

// The list belongs to the conversation it was opened for; a client-side
// switch to another one closes it rather than mixing the two.
watch(
  () => props.conversationId,
  () => popoverRef.value?.hide(),
);

const jobCount = computed(
  () =>
    conversations.value.find((c) => c.conversation_id === props.conversationId)
      ?.running_background_jobs ?? 0,
);
// This conversation's own jobs and direct subagents, like the drawer row's
// badges; a subagent's own jobs and children show on its drawer row.
const subagents = computed(() =>
  conversations.value.filter((c) => c.parent_conversation_id === props.conversationId && c.working),
);

const running = computed(() => jobCount.value > 0 || subagents.value.length > 0);
// The popover unmounts with the last job or subagent, without a hide event.
watch(running, (now) => {
  if (!now) open.value = false;
});

// Icons and numbers only on the bar; the words live here, in the tooltip and
// accessible name.
const tooltip = computed(() => {
  const parts: string[] = [];
  const jobs = jobCount.value;
  const subs = subagents.value.length;
  if (jobs > 0) parts.push(`${jobs} background job${jobs === 1 ? "" : "s"}`);
  if (subs > 0) parts.push(`${subs} subagent${subs === 1 ? "" : "s"}`);
  return `${parts.join(" and ")} running`;
});
</script>
