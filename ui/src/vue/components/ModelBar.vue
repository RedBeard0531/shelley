<!-- Compact one-line generation metadata rendered inside the shared context card. -->
<template>
  <div v-if="model" class="model-bar">
    <div class="model-bar-summary">
      <span class="model-bar-icon" aria-hidden="true">🤖</span>
      <span class="model-bar-label">Model:</span>
      <span class="model-bar-name" :title="modelTitle">{{ displayName }}</span>
      <span class="model-bar-comma" aria-hidden="true">,</span>
      <span class="model-bar-name model-bar-reasoning">{{ effectiveReasoning }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { findModelByName, prettyModelLabels } from "../../utils/modelNames";
import type { Model } from "../../types";

const props = withDefaults(
  defineProps<{
    model?: string | null;
    // Every distinct model the generation actually ran, first-seen order (the
    // first is the starting model). When it holds more than one, the bar shows
    // "Mixed" instead of a single name.
    modelsUsed?: string[];
    models?: Model[];
    thinkingLevel?: string | null;
  }>(),
  { models: () => [], modelsUsed: () => [] },
);

const labels = computed(() => prettyModelLabels(props.models));

// The bar's `model` is the name recorded in usage data — usually the provider's
// wire name (e.g. "accounts/fireworks/models/glm-5p3-flash"), not the Shelley
// id (e.g. "glm-5.3-flash-fireworks"). Resolve it against the model list so
// both the display name and the reasoning default come from the real entry.
const resolved = computed(() => findModelByName(props.models, props.model));

// A generation that ran more than one model (via a mid-generation /model
// switch) shows "Mixed"; the full list is on hover. Otherwise the display name
// comes from the resolved entry — or the recorded name itself, verbatim, when
// the model is no longer configured.
const isMixed = computed(() => props.modelsUsed.length > 1);
const displayName = computed(() => {
  if (isMixed.value) return "Mixed";
  return (resolved.value && labels.value.get(resolved.value.id)) || props.model || "";
});

// Hover spells out the model id, which the display name deliberately hides.
// When the id is all we have (nothing resolved), the tooltip would just repeat
// the label, so it is omitted.
const modelTitle = computed(() => {
  const ids = isMixed.value
    ? props.modelsUsed.map((m) => findModelByName(props.models, m)?.id ?? m)
    : [resolved.value?.id ?? props.model ?? ""];
  const id = ids.join(" \u2192 ");
  return id && id !== displayName.value ? id : undefined;
});

// The reasoning badge is always shown so a conversation never hides how much
// thinking it actually uses. An explicit per-conversation thinking_level wins;
// otherwise fall back to the selected model's default_reasoning_level (what the
// service applies to un-overridden requests). If neither is known — e.g. a
// provider with a dynamic default Shelley can't name — show "default".
const effectiveReasoning = computed(
  () => props.thinkingLevel || resolved.value?.default_reasoning_level || "default",
);
</script>
