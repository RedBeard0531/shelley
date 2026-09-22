<!-- Shared per-model token rows for the main conversation and its sub-agents. -->
<template>
  <div class="token-cost-model-breakdown">
    <div class="token-cost-model-row">
      <span
        class="token-cost-model-name"
        :title="source ? `${usage.model} · ${source}` : usage.model"
        >{{ usage.model }}</span
      >
      <span v-if="usage.priced" class="token-cost-legend-cost">{{
        formatUsd(usage.totalCost)
      }}</span>
      <span v-else-if="usage.reportedUsd > 0" class="token-cost-legend-cost"
        >{{ formatUsd(usage.reportedUsd) }} reported</span
      >
      <span v-else class="token-cost-legend-unit">no pricing</span>
    </div>
    <div v-if="showSource" class="token-cost-model-source">{{ source || "Unknown endpoint" }}</div>
    <div v-for="row in rows" :key="row.band.key" class="token-cost-legend-row">
      <span v-if="showColors" class="token-cost-chip" :style="{ backgroundColor: row.color }" />
      <span class="token-cost-legend-label">{{ row.band.label }}</span>
      <span class="token-cost-legend-tokens">{{ formatTokenCount(row.tokens) }}</span>
      <span v-if="usage.priced" class="token-cost-legend-unit"
        >@ {{ formatUnitPrice(row.unitUsdPerMtok) }}</span
      >
      <span v-if="usage.priced" class="token-cost-legend-cost">{{ formatUsd(row.cost) }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { formatTokenCount, formatUsd, type ModelUsage } from "../../utils/tokenCostGraph";

const props = defineProps<{
  usage: ModelUsage;
  hideZeroCacheWrite: boolean;
  showColors?: boolean;
  source?: string;
  showSource?: boolean;
}>();

// Top-to-bottom mirrors the graph's band stacking order.
const rows = computed(() =>
  props.usage.rows
    .filter(
      (row) =>
        !(props.hideZeroCacheWrite && row.band.costKey === "cache_write" && row.tokens === 0),
    )
    .reverse(),
);

/** Unit price per million tokens. */
function formatUnitPrice(usdPerMtok: number): string {
  let s: string;
  if (Number.isInteger(usdPerMtok)) s = String(usdPerMtok);
  else if (usdPerMtok >= 0.1) s = usdPerMtok.toFixed(2);
  else s = usdPerMtok.toPrecision(2).replace(/\.?0+$/, "");
  return `$${s}/M`;
}
</script>
