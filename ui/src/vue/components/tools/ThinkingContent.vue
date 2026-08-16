<!-- Collapsible chain-of-thought block. Collapsed by default and capped at 10
     rendered lines (see utils/thinkingText.ts); clicking toggles between the
     capped preview and the full text. While streaming (show-tail) the preview
     follows the newest lines instead of the first; when the streamed turn
     finalizes, handoffStreamingThinking() transfers an in-progress expansion
     onto the finalized message's block (via expansion-key), so the preview →
     message swap never collapses a block the user opened and never changes
     their scroll position (both phases cap at the same height).
     Preserves: .thinking-content, .thinking-content-wrapper,
     data-testid thinking-content, .thinking-clickable-area, .thinking-emoji 💭,
     .thinking-text, .thinking-toggle, .thinking-toggle-button. -->
<template>
  <div class="thinking-content thinking-content-wrapper" data-testid="thinking-content">
    <div class="thinking-clickable-area" @click="toggleExpanded">
      <span class="thinking-emoji">💭</span>
      <div ref="textEl" class="thinking-text" :class="isExpanded ? undefined : collapsedClass">
        {{ isExpanded ? thinking : collapsedText }}
      </div>
      <button
        class="thinking-toggle thinking-toggle-button"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import ToolChevron from "./ToolChevron.vue";
import { collapsedThinkingHasMore, collapsedThinkingText } from "../../../utils/thinkingText";
import { getThinkingExpanded, setThinkingExpanded } from "../../../services/thinkingExpansion";

const props = defineProps<{
  thinking: string;
  /** While streaming: the collapsed preview follows the newest lines. */
  showTail?: boolean;
  /** Key that shares the expanded state across remounts (streaming → finalized). */
  expansionKey?: string;
}>();

const isExpanded = ref(false);
const textEl = ref<HTMLElement | null>(null);
let ro: ResizeObserver | null = null;

// By default the collapse cap hides lines; additionally a short slice can clip
// visually when individual lines wrap past one row each. Either means there is
// genuinely more to reveal, so the clip-edge fade shows (see
// collapsedThinkingHasMore; styles.css .thinking-fade-*).
const visuallyClipped = ref(false);
const hasMore = computed(() => collapsedThinkingHasMore(props.thinking) || visuallyClipped.value);

const collapsedClass = computed(() =>
  props.showTail
    ? hasMore.value
      ? "thinking-text-collapsed-tail thinking-fade-top"
      : "thinking-text-collapsed-tail"
    : hasMore.value
      ? "thinking-text-collapsed-head thinking-fade-bottom"
      : "thinking-text-collapsed-head",
);

// Blocks keep their own expanded/collapsed ref, but seed it from (and mirror
// it to) the shared store when an expansionKey is given, so the state survives
// remounts — most importantly the streaming preview → finalized message swap,
// which handoffStreamingThinking() wires up.
onMounted(() => {
  if (props.expansionKey && getThinkingExpanded(props.expansionKey)) {
    isExpanded.value = true;
  }
  // Recheck the visual clip whenever the block or viewport grows.
  const el = textEl.value;
  if (el) {
    const measure = () => {
      visuallyClipped.value = el.scrollHeight > el.clientHeight + 1;
    };
    measure();
    ro = new ResizeObserver(measure);
    ro.observe(el);
  }
});

onUnmounted(() => ro?.disconnect());

function toggleExpanded() {
  isExpanded.value = !isExpanded.value;
  if (props.expansionKey) setThinkingExpanded(props.expansionKey, isExpanded.value);
}

// Collapsed preview: the first (head) or last (tail) MAX_THINKING_PREVIEW_LINES
// newline-lines (see utils/thinkingText.ts). The CSS clamps remain the
// authoritative visual bound for lines that wrap past one visual row each.
const collapsedText = computed(() => collapsedThinkingText(props.thinking, !!props.showTail));

// The ResizeObserver only fires on border-box size changes, so a streaming
// line that wraps past the cap without growing the box (clientHeight pinned at
// max-height) would otherwise leave the clip check stale. Re-measure whenever
// the rendered slice changes.
watch(
  collapsedText,
  () => {
    const el = textEl.value;
    if (el) visuallyClipped.value = el.scrollHeight > el.clientHeight + 1;
  },
  { flush: "post" },
);
</script>
