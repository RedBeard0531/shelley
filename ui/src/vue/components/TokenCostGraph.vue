<!-- Stacked cumulative token-cost graph shown in the context usage popup,
     with a short strip below it showing each call's own cost as a bar.
     X axis: LLM calls or wall-clock time (toggle); in time mode, idle time
     between turns is collapsed into fixed-width gaps. Y axis: cumulative
     dollars per (model, token-band) segment — every segment has its own color
     — falling back to raw token counts when no model in the conversation has
     known pricing. The strip stacks the same segments per call; where calls
     are denser than bars can be drawn, a bar shows its group's most
     expensive call, and hovering picks that call.

     Subagent cost and "other" (indirect) LLM usage — compaction
     summarization, LLM-backed tools, slug generation, … — are not part of the
     graph. Other-usage rows arrive via the otherUsageRows prop (aggregated
     client-side from message other_usage_data) and subagent cost is fetched
     separately. One shared per-model table compares the main conversation
     and sub-agents in aligned columns, with subtotals and a combined total. -->
<template>
  <div class="token-cost-graph">
    <div v-if="loading" class="token-cost-graph-note">Loading pricing…</div>
    <div v-else-if="stack && stack.n > 0" class="token-cost-timeline">
      <div class="token-cost-controls">
        <button
          :class="{ 'token-cost-toggle-active': xMode === 'calls' }"
          class="token-cost-toggle"
          @click="xMode = 'calls'"
        >
          calls
        </button>
        <button
          :class="{ 'token-cost-toggle-active': xMode === 'time' }"
          class="token-cost-toggle"
          @click="xMode = 'time'"
        >
          time
        </button>
        <span class="token-cost-controls-spacer" />
        <slot name="mode-controls" />
      </div>
      <svg
        :viewBox="`0 0 ${W} ${H}`"
        class="token-cost-graph-svg"
        @mousemove="onMove"
        @mouseleave="clearHover"
      >
        <line
          v-for="t in ticks"
          :key="`tick-${t}`"
          :x1="PADL"
          :y1="yAt(t)"
          :x2="W - PADR"
          :y2="yAt(t)"
          class="token-cost-gridline"
        />
        <path v-for="(d, s) in areaPaths" :key="s" :d="d" :fill="stack.segments[s].color" />
        <path v-for="(d, s) in barPaths" :key="`bar-${s}`" :d="d" :fill="stack.segments[s].color" />
        <path
          v-for="i in genStarts"
          :key="`gen-${i}`"
          :d="guide(genX(i))"
          class="token-cost-gen-line"
        />
        <line :x1="PADL" :y1="PADT" :x2="PADL" :y2="AREA_BOTTOM" class="token-cost-axis" />
        <line
          :x1="PADL"
          :y1="AREA_BOTTOM"
          :x2="W - PADR"
          :y2="AREA_BOTTOM"
          class="token-cost-axis"
        />
        <line :x1="PADL" :y1="STRIP_TOP" :x2="PADL" :y2="STRIP_BOTTOM" class="token-cost-axis" />
        <line
          :x1="PADL"
          :y1="STRIP_BOTTOM"
          :x2="W - PADR"
          :y2="STRIP_BOTTOM"
          class="token-cost-axis"
        />
        <path
          v-if="hoverX !== null && stack.n > 1"
          :d="guide(hoverX)"
          class="token-cost-hover-line"
        />
        <text :x="PADL" :y="PADT - 3" class="token-cost-label token-cost-pane-label">
          Cumulative {{ unit }}
          <template v-if="hasSubagents">· main conversation</template>
        </text>
        <text :x="PADL" :y="STRIP_TOP - 3" class="token-cost-label token-cost-pane-label">
          Incremental {{ unit }}
        </text>
        <text
          v-for="t in ticks"
          :key="`ticklabel-${t}`"
          :x="PADL - 3"
          :y="yAt(t) + 3"
          text-anchor="end"
          class="token-cost-label"
        >
          {{ tickLabel(t) }}
        </text>
        <text
          v-if="stack.maxDelta > 0"
          :x="PADL - 3"
          :y="STRIP_TOP + 6"
          text-anchor="end"
          class="token-cost-label"
        >
          {{ tickLabel(stack.maxDelta) }}
        </text>
        <text :x="(PADL + W - PADR) / 2" :y="H - 4" text-anchor="middle" class="token-cost-label">
          {{ xAxisLabel }}
        </text>
      </svg>
      <div class="token-cost-hover-readout">
        <div v-if="hoverText" class="token-cost-hover-text">
          <span>{{ hoverText }}</span>
          <span v-if="hoverSnippet" class="token-cost-hover-snippet"> ({{ hoverSnippet }})</span>
        </div>
        <div v-else>{{ hintText }}</div>
      </div>
      <div v-if="stack.weighted && fetchFailed" class="token-cost-graph-note">
        Pricing lookup failed for some models.
      </div>
      <div v-if="stack.weighted && stack.reportedCostUsd > 0" class="token-cost-graph-note">
        Provider-reported direct cost: {{ formatUsd(stack.reportedCostUsd) }}.
      </div>
      <div v-if="!stack.weighted" class="token-cost-graph-note">
        <template v-if="fetchFailed"> Pricing lookup failed — showing raw token counts. </template>
        <template v-else
          >Raw token counts — no pricing known.<template v-if="stack.reportedCostUsd > 0">
            Provider-reported {{ formatUsd(stack.reportedCostUsd) }}.</template
          ></template
        >
      </div>
    </div>
    <div v-else-if="!loading" class="token-cost-graph-note">No direct usage data yet.</div>
    <table
      v-if="!loading && (stack || otherBreakdown || hasSubagents)"
      class="token-cost-table"
      :class="{ 'token-cost-table-with-subagents': hasSubagents }"
      aria-label="Spend by model"
    >
      <colgroup>
        <col class="token-cost-label-column" />
        <col />
        <col v-if="hasSubagents" />
      </colgroup>
      <thead>
        <tr>
          <th scope="col">Model / tokens</th>
          <th scope="col">Main conversation</th>
          <th v-if="hasSubagents" scope="col">Sub-agents</th>
        </tr>
      </thead>
      <tbody class="token-cost-summary">
        <tr class="token-cost-subtotal-row">
          <th scope="row">Subtotal</th>
          <td data-testid="conversation-cost-subtotal">
            <span
              v-if="mainKnownUsd > 0 || mainUnpricedCalls === 0"
              class="token-cost-legend-cost"
              >{{ formatUsd(mainKnownUsd) }}</span
            >
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </td>
          <td v-if="hasSubagents" data-testid="subagent-cost-row">
            <span
              v-if="subagentKnownUsd > 0 || subagentUsage?.unpriced_calls === 0"
              class="token-cost-legend-cost"
              >{{ formatUsd(subagentKnownUsd) }}</span
            >
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </td>
        </tr>
        <tr
          v-if="!subagentLoading && showCostSummary"
          class="token-cost-total-row"
          data-testid="token-cost-total"
        >
          <th scope="row" :colspan="hasSubagents ? 2 : 1">Total</th>
          <td>
            <span class="token-cost-legend-cost">≈{{ formatUsd(costSummary.totalUsd) }}</span>
          </td>
        </tr>
      </tbody>
      <ModelCostBreakdown
        v-for="model in modelComparison"
        :key="model.model"
        :model="model"
        :show-subagents="hasSubagents"
        :hide-zero-cache-write="hideZeroCacheWrite(model.model)"
      />
      <tbody v-if="otherBreakdown && otherBreakdown.perPurpose.length > 0">
        <tr class="token-cost-model-row">
          <th scope="row"><span class="token-cost-model-name">Other (indirect)</span></th>
          <td>
            <span
              v-if="otherKnownUsd > 0 || otherBreakdown.totals.unpricedCalls === 0"
              class="token-cost-legend-cost"
              >{{ formatUsd(otherKnownUsd) }}</span
            >
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </td>
          <td v-if="hasSubagents" class="token-cost-empty">in model totals</td>
        </tr>
        <tr v-for="p in otherBreakdown.perPurpose" :key="p.purpose" class="token-cost-legend-row">
          <th scope="row">
            <span class="token-cost-legend-label">{{ p.purpose }}</span>
          </th>
          <td>
            <div class="token-cost-cell">
              <span class="token-cost-legend-tokens">{{ formatTokenCount(p.tokens) }}</span>
              <span class="token-cost-legend-unit"
                >{{ p.llmCalls }} {{ p.llmCalls === 1 ? "call" : "calls" }}</span
              >
              <span v-if="otherPurposeKnownUsd(p) > 0 || p.priced" class="token-cost-legend-cost">{{
                formatUsd(otherPurposeKnownUsd(p))
              }}</span>
              <span v-else class="token-cost-empty">no pricing</span>
            </div>
          </td>
          <td v-if="hasSubagents" class="token-cost-empty">—</td>
        </tr>
      </tbody>
    </table>
    <div v-if="!loading && hasSubagents" class="token-cost-graph-note">
      Sub-agent model totals include nested sub-agents and indirect usage.
    </div>
    <div v-if="!loading && subagentLoading" class="token-cost-graph-note">
      Loading total including subagents…
    </div>
    <div
      v-if="!loading && confirmedUnpricedCalls > 0"
      class="token-cost-graph-note token-cost-graph-warning"
    >
      {{ confirmedUnpricedCalls }}
      {{ confirmedUnpricedCalls === 1 ? "call has" : "calls have" }} no model pricing; provider
      reports are included when available, but the total may be incomplete.
    </div>
    <div
      v-if="!loading && subagentFetchFailed"
      class="token-cost-graph-note token-cost-graph-warning"
    >
      Subagent cost unavailable; total may be incomplete.
    </div>
    <div v-if="!loading && !stack && fetchFailed" class="token-cost-graph-note">
      Pricing lookup failed.
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { Model } from "../../types";
import ModelCostBreakdown from "./ModelCostBreakdown.vue";
import {
  modelCostsApi,
  subagentUsageApi,
  type ModelCostDTO,
  type SubagentUsageDTO,
} from "../../services/api";
import {
  buildCostSummary,
  buildModelCostComparison,
  buildOtherUsageBreakdown,
  buildTokenCostStack,
  callXLayout,
  countConfirmedUnpricedCalls,
  deltaBars,
  formatDuration,
  formatTokenCount,
  formatUsd,
  generationBoundary,
  generationStarts,
  timeXLayout,
  yTicks,
  TOKEN_BANDS,
  type DeltaBar,
  type ModelUsage,
  type OtherPurposeUsage,
  type OtherUsageBreakdown,
  type OtherUsageRow,
  type TokenCostStack,
  type UsageEntry,
  type XLayout,
} from "../../utils/tokenCostGraph";

const props = defineProps<{
  entries: UsageEntry[];
  models: Model[];
  otherUsageRows?: OtherUsageRow[];
  conversationId?: string | null;
  active?: boolean;
}>();

// The cumulative area sits on top; the per-call strip below it is kept
// short, since its spikes read fine at a glance. Each pane's label sits in
// the space above it.
const W = 280;
const PADL = 32;
const PADR = 6;
const PADT = 13;
const AREA_H = 112;
const STRIP_GAP = 14;
const STRIP_H = 26;
const PADB = 18;
const AREA_BOTTOM = PADT + AREA_H;
const STRIP_TOP = AREA_BOTTOM + STRIP_GAP;
const STRIP_BOTTOM = STRIP_TOP + STRIP_H;
const H = STRIP_BOTTOM + PADB;

const loading = ref(false);
const fetchFailed = ref(false);
const costs = ref<Record<string, ModelCostDTO | null>>({});

// "Other" (indirect) LLM usage — compaction, LLM-backed tools, slug
// generation, … — arrives pre-aggregated via the otherUsageRows prop. It has
// no per-call timeline, so it stays out of the graph and only appears in the
// breakdown and the note line; its models join the pricing batch below.

// (model, url) pairs to price: the graph's entries plus other-usage models,
// deduped by model name (first-seen URL wins).
const distinctModels = computed(() => {
  const seen = new Map<string, string>();
  for (const e of props.entries) {
    if (e.model && !seen.has(e.model)) seen.set(e.model, e.url || "");
  }
  for (const row of props.otherUsageRows ?? []) {
    if (row.model && !seen.has(row.model)) seen.set(row.model, row.url || "");
  }
  return seen;
});

let pricingRequest = 0;
watch(
  distinctModels,
  async (models) => {
    const request = ++pricingRequest;
    if (models.size === 0) {
      costs.value = {};
      fetchFailed.value = false;
      loading.value = false;
      return;
    }
    loading.value = Object.keys(costs.value).length === 0;
    try {
      const nextCosts = await modelCostsApi.lookup(
        Array.from(models).map(([model, url]) => ({ model, url })),
      );
      if (request !== pricingRequest) return;
      costs.value = nextCosts;
      fetchFailed.value = false;
    } catch (e) {
      if (request !== pricingRequest) return;
      console.warn("model costs lookup failed", e);
      fetchFailed.value = true;
    } finally {
      if (request === pricingRequest) loading.value = false;
    }
  },
  { immediate: true },
);

const stack = computed<TokenCostStack | null>(() =>
  props.entries.length > 0 ? buildTokenCostStack(props.entries, costs.value) : null,
);

// Subagent usage is aggregated server-side (a recursive query over descendant
// conversations) and shown in its own model breakdown, not in the graph. Refresh
// on parent activity and while the popup is open so a running child's spend
// does not leave the emphasized total stale.
const subagentUsage = ref<SubagentUsageDTO | null>(null);
const subagentLoading = ref(false);
const subagentFetchFailed = ref(false);
let subagentRequest = 0;
let subagentAbort: AbortController | null = null;

function cancelSubagentUsage(): void {
  subagentAbort?.abort();
  subagentAbort = null;
  subagentLoading.value = false;
}

async function loadSubagentUsage(showLoading: boolean): Promise<void> {
  const id = props.conversationId;
  if (!id) {
    cancelSubagentUsage();
    subagentUsage.value = null;
    return;
  }
  // Polls never stack. Conversation changes cancel explicitly before loading.
  if (subagentAbort) return;
  const controller = new AbortController();
  subagentAbort = controller;
  const request = ++subagentRequest;
  if (showLoading) subagentLoading.value = true;
  try {
    const usage = await subagentUsageApi.get(id, controller.signal);
    if (request !== subagentRequest) return;
    subagentUsage.value = usage;
    subagentFetchFailed.value = false;
  } catch (e) {
    if ((e as Error).name === "AbortError") return;
    console.warn("subagent usage lookup failed", e);
    if (request === subagentRequest) subagentFetchFailed.value = true;
  } finally {
    if (subagentAbort === controller) subagentAbort = null;
    if (request === subagentRequest) subagentLoading.value = false;
  }
}

watch(
  () => props.conversationId,
  () => {
    cancelSubagentUsage();
    subagentFetchFailed.value = false;
    subagentUsage.value = null;
    void loadSubagentUsage(true);
  },
  { immediate: true },
);

watch(
  () =>
    [
      props.entries.length,
      (props.otherUsageRows ?? []).reduce((sum, row) => sum + row.llm_calls, 0),
    ] as const,
  () => {
    if (subagentUsage.value) void loadSubagentUsage(false);
  },
);

const SUBAGENT_REFRESH_MS = 5_000;
let subagentRefreshTimer: number | null = null;
function stopSubagentRefresh(): void {
  if (subagentRefreshTimer === null) return;
  window.clearInterval(subagentRefreshTimer);
  subagentRefreshTimer = null;
}
watch(
  () => props.active,
  (active) => {
    stopSubagentRefresh();
    if (!active) {
      cancelSubagentUsage();
      return;
    }
    if (!subagentLoading.value) void loadSubagentUsage(subagentUsage.value === null);
    subagentRefreshTimer = window.setInterval(
      () => void loadSubagentUsage(false),
      SUBAGENT_REFRESH_MS,
    );
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  stopSubagentRefresh();
  cancelSubagentUsage();
});

const subagentKnownUsd = computed(() => {
  const sub = subagentUsage.value;
  return sub ? sub.estimated_usd + sub.unpriced_reported_usd : 0;
});

const hasSubagents = computed(() => (subagentUsage.value?.llm_calls ?? 0) > 0);

// Pricing travels with each (model, endpoint) aggregate so its token costs
// reconcile with the server subtotal, including endpoint-specific pricing.
const subagentModels = computed<ModelUsage[]>(() =>
  (subagentUsage.value?.per_model ?? []).map((row) => ({
    model: row.model || "unknown model",
    priced: row.cost !== null,
    totalCost: row.estimated_usd,
    reportedUsd: row.reported_usd,
    rows: TOKEN_BANDS.map((band) => ({
      band,
      tokens: row[band.key],
      unitUsdPerMtok: row.cost?.[band.costKey] ?? 0,
      cost: (row[band.key] * (row.cost?.[band.costKey] ?? 0)) / 1e6,
      color: "",
    })),
  })),
);

const modelComparison = computed(() =>
  buildModelCostComparison(stack.value?.perModel ?? [], subagentModels.value),
);

const otherBreakdown = computed<OtherUsageBreakdown | null>(() => {
  const rows = props.otherUsageRows;
  if (!rows || rows.length === 0) return null;
  return buildOtherUsageBreakdown(rows, costs.value);
});

const otherKnownUsd = computed(() => {
  const totals = otherBreakdown.value?.totals;
  return totals ? totals.estimatedUsd + totals.reportedUnpricedUsd : 0;
});

function otherPurposeKnownUsd(purpose: OtherPurposeUsage): number {
  return purpose.estimatedUsd + purpose.reportedUnpricedUsd;
}

const costSummary = computed(() => {
  const s = stack.value;
  const other = otherBreakdown.value?.totals;
  const subagents = subagentUsage.value;
  const conversationUnpricedCalls = s
    ? props.entries.filter((entry) => !entry.model || !costs.value[entry.model]).length
    : 0;
  const conversationUnpricedReportedUsd = s
    ? s.perModel.filter((model) => !model.priced).reduce((sum, model) => sum + model.reportedUsd, 0)
    : 0;
  const conversationEstimatedUsd = s?.weighted ? s.maxY : 0;
  return buildCostSummary(conversationEstimatedUsd + conversationUnpricedReportedUsd, {
    conversationUnpricedCalls,
    other: other
      ? {
          estimatedUsd: other.estimatedUsd,
          reportedUnpricedUsd: other.reportedUnpricedUsd,
          unpricedCalls: other.unpricedCalls,
        }
      : undefined,
    subagents: subagents
      ? {
          estimatedUsd: subagents.estimated_usd,
          reportedUnpricedUsd: subagents.unpriced_reported_usd,
          unpricedCalls: subagents.unpriced_calls,
        }
      : undefined,
  });
});

const mainKnownUsd = computed(() => costSummary.value.conversationUsd + costSummary.value.otherUsd);
const mainUnpricedCalls = computed(
  () => costSummary.value.conversationUnpricedCalls + costSummary.value.otherUnpricedCalls,
);

// A failed lookup is unavailable pricing, not a confirmed unpriced model.
// The server-priced sub-agent results remain independent of that request.
const confirmedUnpricedCalls = computed(
  () =>
    costSummary.value.subagentUnpricedCalls +
    countConfirmedUnpricedCalls(props.entries, costs.value) +
    countConfirmedUnpricedCalls(props.otherUsageRows ?? [], costs.value),
);

const showCostSummary = computed(() => {
  const other = otherBreakdown.value?.totals;
  const subagents = subagentUsage.value;
  return (
    !!stack.value?.weighted ||
    costSummary.value.totalUsd > 0 ||
    (other && other.llmCalls > other.unpricedCalls) ||
    (subagents && subagents.llm_calls > subagents.unpriced_calls)
  );
});

const plotW = W - PADL - PADR;

const xMode = ref<"calls" | "time">("calls");

const layout = computed<XLayout>(() =>
  xMode.value === "time" ? timeXLayout(props.entries) : callXLayout(stack.value?.n ?? 0),
);

function xAt(i: number): number {
  return PADL + layout.value.xs[i] * plotW;
}

function yAt(v: number): number {
  return PADT + AREA_H * (1 - v / (stack.value?.maxY || 1));
}

function stripYAt(v: number): number {
  return STRIP_TOP + STRIP_H * (1 - v / (stack.value?.maxDelta || 1));
}

const unit = computed(() => (stack.value?.weighted ? "cost" : "tokens"));

// A vertical guide through both panes, skipping the label between them.
function guide(x: number): string {
  const px = x.toFixed(1);
  return `M${px},${PADT}V${AREA_BOTTOM}M${px},${STRIP_TOP}V${STRIP_BOTTOM}`;
}

function genX(i: number): number {
  return PADL + generationBoundary(bars.value, layout.value.xs, i) * plotW;
}

// One path per (model, band) segment; each turn is a separate subpath so
// idle time between turns renders as a gap in time mode. Zero-width turns
// (single call, or all calls sharing one second-granularity timestamp)
// become narrow slabs so they stay visible.
const areaPaths = computed<string[]>(() => {
  const s = stack.value;
  const lay = layout.value;
  if (!s || s.n === 0 || s.maxY === 0) return [];
  const px = (i: number) => (PADL + lay.xs[i] * plotW).toFixed(1);
  const lower = (si: number, i: number) => (si === 0 ? 0 : s.layers[si - 1][i]);
  return s.segments.map((_, si) => {
    let d = "";
    for (const [a, b] of lay.turns) {
      if (lay.xs[a] === lay.xs[b]) {
        const x = PADL + lay.xs[a] * plotW;
        const hw = Math.max(1, plotW * 0.006);
        const x0 = Math.max(PADL, x - hw).toFixed(1);
        const x1 = Math.min(W - PADR, x + hw).toFixed(1);
        const yT = yAt(s.layers[si][b]).toFixed(1);
        const yB = yAt(lower(si, b)).toFixed(1);
        d += `M${x0},${yT}L${x1},${yT}L${x1},${yB}L${x0},${yB}Z`;
        continue;
      }
      const top: string[] = [];
      for (let i = a; i <= b; i++) top.push(`${px(i)},${yAt(s.layers[si][i]).toFixed(1)}`);
      const bottom: string[] = [];
      for (let i = b; i >= a; i--) bottom.push(`${px(i)},${yAt(lower(si, i)).toFixed(1)}`);
      d += `M${top.join("L")}L${bottom.join("L")}Z`;
    }
    return d;
  });
});

// Non-overlapping per-call bars; each draws (and hovers as) one call.
const bars = computed<DeltaBar[]>(() => {
  const s = stack.value;
  if (!s || s.n === 0 || s.segments.length === 0) return [];
  return deltaBars(layout.value.xs, s.deltaLayers[s.segments.length - 1]);
});

// One path per segment holding a rect per bar with a nonzero share.
const barPaths = computed<string[]>(() => {
  const s = stack.value;
  if (!s || s.maxDelta === 0) return [];
  const xs = bars.value.map((b) => [
    (PADL + b.left * plotW).toFixed(2),
    (PADL + b.right * plotW).toFixed(2),
  ]);
  return s.segments.map((_, si) => {
    let d = "";
    bars.value.forEach((b, k) => {
      const top = s.deltaLayers[si][b.call];
      const bottom = si === 0 ? 0 : s.deltaLayers[si - 1][b.call];
      if (top <= bottom) return;
      const [x0, x1] = xs[k];
      const yT = stripYAt(top).toFixed(1);
      const yB = stripYAt(bottom).toFixed(1);
      d += `M${x0},${yB}V${yT}H${x1}V${yB}Z`;
    });
    return d;
  });
});

const ticks = computed<number[]>(() => yTicks(stack.value?.maxY ?? 0));

function tickLabel(v: number): string {
  if (!stack.value?.weighted) return formatTokenCount(v);
  if (v >= 10) return `$${Math.round(v)}`;
  if (v >= 1) return `$${parseFloat(v.toFixed(1))}`;
  if (v >= 0.01) return `$${parseFloat(v.toFixed(2))}`;
  return formatUsd(v);
}

const xAxisLabel = computed(() => {
  const s = stack.value;
  if (!s) return "";
  if (xMode.value === "calls") return "LLM call";
  const lay = layout.value;
  const dur = lay.activeMs > 0 ? `${formatDuration(lay.activeMs)} active` : "time";
  return lay.turns.length > 1 ? `${dur} · gaps = idle between turns` : dur;
});

const hintText = computed(() => (genStarts.value.length ? "Dashed lines mark compactions." : ""));

// ChatGPT subscriptions report zero writes even when caching works.
// Usage uses native model names, not picker IDs. A graph group can combine
// endpoints; hide zero writes only when every contribution is subscription-backed.
const subscriptionOnly = computed(() => {
  const result = new Map<string, boolean>();
  for (const usage of props.entries) {
    if (!usage.model || result.get(usage.model) === false) continue;
    result.set(usage.model, isSubscriptionUsage(usage.model, usage.url));
  }
  return result;
});

function hideZeroCacheWrite(name: string): boolean {
  if (
    stack.value?.perModel.some((model) => model.model === name) &&
    !subscriptionOnly.value.get(name)
  )
    return false;
  return (subagentUsage.value?.per_model ?? [])
    .filter((row) => (row.model || "unknown model") === name)
    .every((row) => isSubscriptionUsage(row.model, row.url));
}

function isSubscriptionUsage(name: string, url?: string): boolean {
  const sources = props.models.filter(
    (model) =>
      model.api_model_name === name &&
      model.base_url &&
      (url === model.base_url || url?.startsWith(`${model.base_url}/`)),
  );
  return sources.length > 0 && sources.every((model) => model.mode === "chatgpt");
}

// Pointer position as a plot fraction. The hovered call is derived from it,
// so a redraw (pricing arriving, axis switch) can't leave the readout
// describing a different call than the one drawn under the pointer.
const hoverFrac = ref<number | null>(null);

function clearHover() {
  hoverFrac.value = null;
}

// Switching conversations swaps the graph out from under a still pointer.
watch(() => stack.value?.n, clearHover);

// The call under the pointer: the one its per-call bar draws, so the readout
// always matches the bar beneath the cursor. Every call lies inside its bar,
// so the hover line (at the call's x) does too.
const hoverIndex = computed<number | null>(() => {
  const frac = hoverFrac.value;
  if (frac === null) return null;
  let best: DeltaBar | null = null;
  let bestD = Infinity;
  for (const b of bars.value) {
    const d = Math.max(b.left - frac, frac - b.right, 0);
    if (d < bestD) {
      bestD = d;
      best = b;
    }
  }
  return best?.call ?? null;
});

const hoverX = computed(() => (hoverIndex.value === null ? null : xAt(hoverIndex.value)));

function onMove(ev: MouseEvent) {
  const rect = (ev.currentTarget as SVGSVGElement).getBoundingClientRect();
  const px = ((ev.clientX - rect.left) / rect.width) * W;
  hoverFrac.value = (px - PADL) / plotW;
}

const genStarts = computed(() => generationStarts(props.entries));

function amount(v: number): string {
  return stack.value?.weighted ? formatUsd(v) : `${formatTokenCount(v)} tok`;
}

// "call 169, $1.61, cumulative $47.05", then the snippet: "(bash)". Time
// mode adds the time.
const hoverText = computed(() => {
  const s = stack.value;
  const i = hoverIndex.value;
  if (!s || i === null) return "";
  const e = props.entries[i];
  const top = s.segments.length - 1;
  const parts = [`call ${i + 1}`];
  if (xMode.value === "time" && e.timestamp) {
    const d = new Date(e.timestamp);
    const day = d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
    parts.push(`${day} ${d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`);
  }
  // A call to an unpriced model adds nothing to a dollar graph, but it is
  // not free: say so, with the provider's own figure when there is one.
  if (s.weighted && !(e.model && costs.value[e.model])) {
    parts.push(e.cost_usd > 0 ? `${formatUsd(e.cost_usd)} reported` : "unpriced");
  } else {
    parts.push(amount(s.deltaLayers[top][i]));
  }
  parts.push(`cumulative ${amount(s.layers[top][i])}`);
  return parts.join(", ");
});

const hoverSnippet = computed(() =>
  hoverIndex.value === null ? "" : props.entries[hoverIndex.value]?.snippet || "",
);
</script>
