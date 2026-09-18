<!-- The token-count segment of the status readout ("15k"). Opens token/cost
     graphs and manual compaction actions. The count warms up at 100k / 200k /
     300k tokens (or 70/80/90% of a known context window) and the popup
     auto-opens once per browser at the first step. -->
<template>
  <div ref="barRef" class="context-usage-root">
    <Popover
      ref="popoverRef"
      :pt="{
        root: {
          class: 'chat-context-popup',
          id: popupId,
          'aria-label': 'Context usage',
          style: { '--context-popup-available-height': popupAvailableHeight },
        },
        content: { class: 'chat-context-popup-content' },
      }"
      @show="onPopupShow"
      @hide="popupOpen = false"
    >
      <div class="usage-popup-header">
        <div class="usage-popup-title">{{ popupTitle }}</div>
        <UsageGraphSwitch v-if="usageEntries && usageEntries.length > 0" v-model="panes" />
      </div>
      <TokenCostGraph
        v-if="popupOpen"
        :entries="usageEntries || []"
        :messages="messages || []"
        :models="models"
        :other-usage-rows="otherUsageRows || []"
        :conversation-id="conversationId"
        :panes="panes"
        active
      />
      <div v-if="showLongConversationWarning" class="chat-popup-warning">
        This conversation is getting long.
        <br />
        Compact it or start a new conversation.
      </div>
      <div
        v-if="
          conversationId && (onDistillNewGeneration || onStartNewGeneration || onCompactInPlace)
        "
        class="chat-distill-container"
      >
        <button
          v-if="onDistillNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleDistillNewGeneration"
        >
          {{ distilling ? "Compacting..." : "Compact Conversation" }}
        </button>
        <button
          v-if="onCompactInPlace"
          :disabled="distilling || compactInPlaceBusy"
          class="chat-distill-button chat-distill-generation-button"
          data-testid="compact-in-place-button"
          :aria-describedby="compactInPlaceBusy ? compactInPlaceHintId : undefined"
          @click="handleCompactInPlace"
        >
          Compact in Place
        </button>
        <span
          v-if="onCompactInPlace && compactInPlaceBusy"
          :id="compactInPlaceHintId"
          class="token-cost-graph-note"
        >
          Finish or stop the current turn to compact in place.
        </span>
        <button
          v-if="onStartNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleStartNewGeneration"
        >
          Start New Generation
        </button>
      </div>
    </Popover>
    <button
      type="button"
      class="context-usage-label status-readout-control"
      :aria-label="usageTitle"
      aria-haspopup="dialog"
      :aria-expanded="popupOpen"
      :aria-controls="popupOpen ? popupId : undefined"
      v-tooltip.top="usageTooltip"
      @pointerenter="props.onUsageNeeded?.()"
      @focus="props.onUsageNeeded?.()"
      @click="openPopup($event)"
    >
      <span :class="['context-usage-label-tokens', 'status-readout-affordance', usageLevelClass]">{{
        formatTokenCount(contextWindowSize)
      }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import Popover from "primevue/popover";
import type { Message, Model } from "../../types";
import { contextUsageLevel, contextUsageLevelLabel } from "../../utils/contextUsage";
import { formatTokenCount } from "../../utils/tokenCostGraph";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import { useUsagePanesPreference } from "../composables/usagePanesPreference";
import TokenCostGraph from "./TokenCostGraph.vue";
import UsageGraphSwitch from "./UsageGraphSwitch.vue";

const props = defineProps<{
  contextWindowSize: number;
  /** Model context window (models.dev, pricing-tier clamped); 0 when unknown.
   *  Never displayed — only floors the warning color as the window fills. */
  maxContextTokens: number;
  conversationId?: string | null;
  usageEntries?: UsageEntry[];
  // Model list, for labelling the usage names recorded in the data (see
  // findModelByName).
  models: Model[];
  otherUsageRows?: OtherUsageRow[];
  messages?: Message[];
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration?: () => Promise<void> | void;
  /** Asks the agent to use compact_in_place, enabling the tool first if the
   *  conversation was started without it. */
  onCompactInPlace?: () => Promise<void> | void;
  /** The tool still has to be enabled, which waits for the turn to end. */
  compactInPlaceBusy?: boolean;
  /** Called just before the popup opens. The parent computes usageEntries /
   *  otherUsageRows lazily (walking every message and parsing its usage data),
   *  so it needs a beat's warning; the graph renders empty for one tick and
   *  fills in on the next. */
  onUsageNeeded?: () => void;
  agentWorking?: boolean;
}>();

const distilling = ref(false);
// Which graphs the popup stacks; remembered across page loads.
const { panes } = useUsagePanesPreference();
const popupOpen = ref(false);
const popupAvailableHeight = ref<string>();
const popupId = useId();
const compactInPlaceHintId = useId();
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const barRef = ref<HTMLElement | null>(null);

// The token count is the whole warning: it warms up (amber, orange, red) as
// the conversation grows, rather than a triangle appearing beside it.
const usageLevel = computed(() =>
  contextUsageLevel(props.contextWindowSize, props.maxContextTokens),
);
const usageLevelClass = computed(() =>
  usageLevel.value ? `context-usage-label-tokens-${usageLevel.value}` : "",
);
// The popup's advice and its once-per-browser auto-open fire exactly when the
// count first colors.
const showLongConversationWarning = computed(() => usageLevel.value !== "");
let hasAutoOpened = false;

// Spelled out for the accessible name; the level is named in words too since
// hue alone is no signal for some readers.
const usageTitle = computed(() => {
  const level = contextUsageLevelLabel(usageLevel.value);
  const suffix = level ? ` — conversation ${level}` : "";
  return `Context usage: ${formatTokenCount(props.contextWindowSize)} tokens${suffix}`;
});
const popupTitle = computed(
  () => `Current context: ${formatTokenCount(props.contextWindowSize)} tokens`,
);
const usageTooltip = computed(() => `${usageTitle.value}. Click for details.`);

// Warn the parent as early as we can — hover/focus, which precede the click —
// so the usage walk has usually landed by the time the graph mounts.
function openPopup(event: Event) {
  props.onUsageNeeded?.();
  popoverRef.value?.toggle(event);
}

// Every path that makes the graph visible funnels through the Popover's show
// event, including the programmatic auto-open below, so ask again here: the
// hover/focus/click hints above are an optimization, this is the guarantee.
function onPopupShow() {
  // Keep a tall breakdown above its trigger, including on short viewports.
  // Otherwise Popover clamps it to the viewport top and can cover the label.
  if (barRef.value) {
    popupAvailableHeight.value = `${Math.max(0, barRef.value.getBoundingClientRect().top - 16)}px`;
  }
  popupOpen.value = true;
  props.onUsageNeeded?.();
}

// This component is not remounted on a conversation switch, but the parent
// resets its lazy usage gate on one, so a popup that was already open would
// keep showing the empty graph until dismissed and reopened. Re-ask.
watch(
  () => props.conversationId,
  () => {
    if (popupOpen.value) props.onUsageNeeded?.();
  },
);

// Auto-open popup once per browser at the long-conversation threshold.
// Programmatic open: PrimeVue's show() anchors to event.currentTarget, so pass
// the usage label element explicitly as the target.
watch(
  [showLongConversationWarning, () => props.agentWorking, () => props.conversationId],
  () => {
    const isMobile = window.innerWidth <= 768;
    if (
      showLongConversationWarning.value &&
      !props.agentWorking &&
      !isMobile &&
      props.conversationId &&
      !hasAutoOpened &&
      localStorage.getItem("shelley_long_convo_popup_shown") !== "1"
    ) {
      hasAutoOpened = true;
      // Wait a tick: with { immediate: true } this can fire before mount,
      // when barRef/popoverRef are still null. Only burn the once-per-browser
      // localStorage flag if the popup actually opens.
      void nextTick(() => {
        const anchor = barRef.value?.querySelector<HTMLElement>(".context-usage-label");
        if (!anchor || !popoverRef.value) return;
        localStorage.setItem("shelley_long_convo_popup_shown", "1");
        popoverRef.value.show(new Event("click"), anchor);
      });
    }
  },
  { immediate: true },
);

async function handleDistillNewGeneration() {
  if (distilling.value || !props.onDistillNewGeneration) return;
  distilling.value = true;
  try {
    await props.onDistillNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}

async function handleCompactInPlace() {
  if (distilling.value || !props.onCompactInPlace) return;
  distilling.value = true;
  try {
    await props.onCompactInPlace();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}

async function handleStartNewGeneration() {
  if (distilling.value || !props.onStartNewGeneration) return;
  distilling.value = true;
  try {
    await props.onStartNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}
</script>
