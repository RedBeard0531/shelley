<!-- Vue port of components/UsageDetailModal.tsx. Token/cost/duration breakdown
     for an agent message's usage data. Uses the shared Modal (PrimeVue Dialog)
     for the chrome; the .usage-detail-grid/-label/-value content classes are
     preserved. Mounted only while open (parent v-if), so isOpen is constant. -->
<template>
  <Modal
    :is-open="true"
    title="Usage Details"
    class-name="usage-detail-modal"
    @close="emit('close')"
  >
    <div class="usage-detail-grid">
      <template v-if="usage.model">
        <div class="usage-detail-label">Model:</div>
        <div class="usage-detail-value">{{ usage.model }}</div>
      </template>
      <div class="usage-detail-label usage-detail-total-label">Prompt Tokens (total):</div>
      <div class="usage-detail-value usage-detail-total-value">
        {{ promptTokens.toLocaleString() }}
      </div>
      <div class="usage-detail-sub-label">Uncached Input</div>
      <div class="usage-detail-sub-value">{{ usage.input_tokens.toLocaleString() }}</div>
      <div class="usage-detail-sub-label">Cache Read (included above)</div>
      <div class="usage-detail-sub-value">
        {{ usage.cache_read_input_tokens.toLocaleString() }}
      </div>
      <div class="usage-detail-sub-label">Cache Write (included above)</div>
      <div class="usage-detail-sub-value">
        {{ usage.cache_creation_input_tokens.toLocaleString() }}
      </div>
      <div class="usage-detail-label usage-detail-total-label">Output Tokens (total):</div>
      <div class="usage-detail-value usage-detail-total-value">
        {{ usage.output_tokens.toLocaleString() }}
      </div>
      <template v-if="reasoningBreakdownAvailable">
        <div class="usage-detail-sub-label">Reasoning (included above)</div>
        <div class="usage-detail-sub-value">{{ usage.reasoning_tokens!.toLocaleString() }}</div>
        <div class="usage-detail-sub-label">Non-reasoning output (remainder)</div>
        <div class="usage-detail-sub-value">
          {{ (usage.output_tokens - usage.reasoning_tokens!).toLocaleString() }}
        </div>
      </template>
      <template v-else>
        <div class="usage-detail-sub-label">Reasoning breakdown</div>
        <div
          class="usage-detail-sub-value usage-detail-unavailable"
          title="The provider did not report a usable reasoning-token count."
        >
          Unavailable
        </div>
      </template>
      <template v-if="usage.cost_usd > 0">
        <div class="usage-detail-label">Reported Cost:</div>
        <div class="usage-detail-value">{{ formatUsd(usage.cost_usd) }}</div>
      </template>
      <div class="usage-detail-label">Estimated Cost (models.dev):</div>
      <div class="usage-detail-value">
        <template v-if="pricingStatus === 'ready'">{{ formatUsd(estimatedCost!) }}</template>
        <span v-else-if="pricingStatus === 'loading'">Loading…</span>
        <span v-else-if="pricingStatus === 'error'" role="alert">
          Unavailable: {{ pricingError }}
        </span>
        <span v-else class="usage-detail-unavailable">No model pricing available</span>
      </div>
      <template v-if="durationMs !== null">
        <div class="usage-detail-label">Duration:</div>
        <div class="usage-detail-value">{{ formatDuration(durationMs!) }}</div>
      </template>
      <template v-if="usage.end_time">
        <div class="usage-detail-label">Timestamp:</div>
        <div class="usage-detail-value">{{ formatTimestamp(usage.end_time) }}</div>
      </template>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { modelCostsApi, type ModelCostDTO } from "../../services/api";
import type { Usage } from "../../types";
import { estimateUsageCost, formatUsd, totalPromptTokens } from "../../utils/tokenCostGraph";
import Modal from "./Modal.vue";

const props = defineProps<{
  usage: Usage;
  durationMs: number | null;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const promptTokens = computed(() => totalPromptTokens(props.usage));
const reasoningBreakdownAvailable = computed(
  () =>
    props.usage.reasoning_tokens !== undefined &&
    props.usage.reasoning_tokens <= props.usage.output_tokens,
);
const modelCost = ref<ModelCostDTO | null>(null);
const pricingStatus = ref<"loading" | "ready" | "unavailable" | "error">("loading");
const pricingError = ref("");
const estimatedCost = computed(() =>
  modelCost.value ? estimateUsageCost(props.usage, modelCost.value) : null,
);

onMounted(async () => {
  const model = props.usage.model;
  if (!model) {
    pricingStatus.value = "unavailable";
    return;
  }

  try {
    const costs = await modelCostsApi.lookup([{ model, url: props.usage.url ?? "" }]);
    modelCost.value = costs[model] ?? null;
    pricingStatus.value = modelCost.value ? "ready" : "unavailable";
  } catch (error) {
    pricingError.value = error instanceof Error ? error.message : String(error);
    pricingStatus.value = "error";
  }
});

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 60000).toFixed(2)}m`;
}

function formatTimestamp(isoString: string): string {
  const date = new Date(isoString);
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}
</script>

<style scoped>
.usage-detail-total-label,
.usage-detail-total-value {
  font-weight: 650;
}

.usage-detail-sub-label {
  padding-inline-start: 1rem;
  color: var(--text-secondary);
}

.usage-detail-sub-value {
  color: var(--text-primary);
}

.usage-detail-unavailable {
  color: var(--text-tertiary);
}
</style>
