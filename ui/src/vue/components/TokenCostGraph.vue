<!-- The graphs in the context usage popup, stacked on one shared x axis and
     switched on and off independently: context length (colored by estimated
     composition), cumulative cost, and each call's own cost as a short strip.
     Each pane is labeled down its left side. X axis: LLM calls or wall-clock
     time (toggle); in time mode, idle time between turns is collapsed into
     fixed-width gaps. Cost Y axes are cumulative dollars per (model,
     token-band) segment — every segment has its own color — falling back to
     raw token counts when no model in the conversation has known pricing. The
     strip stacks the same segments per call; where calls are denser than bars
     can be drawn, a bar shows its group's most expensive call, and hovering
     picks that call. A hover guide runs through every pane, and the readout
     and context legend follow it.

     Subagent cost and "other" (indirect) LLM usage — compaction
     summarization, LLM-backed tools, slug generation, … — are not part of the
     graphs. Other-usage rows arrive via the otherUsageRows prop (aggregated
     client-side from message other_usage_data) and subagent cost is fetched
     separately. One shared per-model table compares the main conversation
     and sub-agents in aligned columns, with a combined total and subtotals. -->
<template>
  <div class="token-cost-graph">
    <div v-if="stack && stack.n > 0">
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
      </div>
      <div v-if="pricingPending" class="token-cost-graph-note">Loading pricing…</div>
      <template v-else>
        <svg
          v-if="paneViews.length > 0"
          ref="svgRef"
          :viewBox="`0 0 ${W} ${H}`"
          role="group"
          :aria-label="`Usage graphs across ${stack.n} LLM calls`"
          class="token-cost-graph-svg"
          @mousemove="onMove"
          @mouseleave="clearHover"
        >
          <g v-for="pane in paneViews" :key="pane.id" :data-pane="pane.id">
            <line
              v-for="tick in pane.ticks.filter((t) => t.grid)"
              :key="`tick-${tick.value}`"
              :x1="PADL"
              :y1="tick.y"
              :x2="W - PADR"
              :y2="tick.y"
              class="token-cost-gridline"
            />
            <path v-for="(d, s) in pane.paths" :key="s" :d="d" :fill="pane.colors[s]" />
            <line
              :x1="PADL"
              :y1="pane.box.top"
              :x2="PADL"
              :y2="pane.box.bottom"
              class="token-cost-axis"
            />
            <line
              :x1="PADL"
              :y1="pane.box.bottom"
              :x2="W - PADR"
              :y2="pane.box.bottom"
              class="token-cost-axis"
            />
            <g role="img" :aria-label="pane.label.join(' ')" class="token-cost-pane-title">
              <text
                v-for="(line, k) in pane.label"
                :key="k"
                :transform="`translate(${LABEL_X[k]},${pane.box.top + pane.box.height / 2}) rotate(-90)`"
                text-anchor="middle"
                aria-hidden="true"
                class="token-cost-label token-cost-pane-label"
              >
                {{ line }}
              </text>
            </g>
            <text
              v-for="tick in pane.ticks"
              :key="`ticklabel-${tick.value}`"
              :x="PADL - 3"
              :y="tick.y + 3"
              text-anchor="end"
              class="token-cost-label"
            >
              {{ tick.label }}
            </text>
          </g>
          <path
            v-for="i in markStarts"
            :key="`mark-${i}`"
            :d="guide(markX(i))"
            class="token-cost-gen-line"
          />
          <path
            v-for="m in cacheMissMarkers"
            :key="`miss-${m.kind}-${m.index}`"
            :d="guide(markX(m.index))"
            :class="m.kind === 'model' ? 'token-cost-model-line' : 'token-cost-miss-line'"
          />
          <path
            v-if="hoverX !== null && stack.n > 1"
            :d="guide(hoverX)"
            class="token-cost-hover-line"
          />
          <text :x="(PADL + W - PADR) / 2" :y="H - 4" text-anchor="middle" class="token-cost-label">
            {{ xAxisLabel }}
          </text>
        </svg>
        <div v-if="paneViews.length > 0" class="token-cost-hover-readout">
          <div v-if="hoverText" class="token-cost-hover-text">
            <span>{{ hoverText }}</span>
            <span v-if="hoverSnippet" class="token-cost-hover-snippet"> ({{ hoverSnippet }})</span>
          </div>
          <div v-else>{{ hintText }}</div>
        </div>
        <ContextLegend
          v-if="showsContext && legendPoint"
          :categories="categories"
          :point="legendPoint"
        />
        <div v-if="showsContext && legendPoint" class="token-cost-graph-note">
          The context mix is estimated from message contents.
        </div>
        <div v-if="hasSubagents && paneViews.length > 0" class="token-cost-graph-note">
          Graphs show the main conversation only.
        </div>
        <template v-if="needsPricing">
          <div v-if="stack.weighted && fetchFailed" class="token-cost-graph-note">
            Pricing lookup failed for some models.
          </div>
          <div v-if="stack.weighted && stack.reportedCostUsd > 0" class="token-cost-graph-note">
            Provider-reported direct cost: {{ formatUsd(stack.reportedCostUsd) }}.
          </div>
          <div v-if="!stack.weighted" class="token-cost-graph-note">
            <template v-if="fetchFailed">
              Pricing lookup failed — showing raw token counts.
            </template>
            <template v-else
              >Raw token counts — no pricing known.<template v-if="stack.reportedCostUsd > 0">
                Provider-reported {{ formatUsd(stack.reportedCostUsd) }}.</template
              ></template
            >
          </div>
        </template>
      </template>
    </div>
    <div v-else-if="loading" class="token-cost-graph-note">Loading pricing…</div>
    <div v-else class="token-cost-graph-note">No direct usage data yet.</div>
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
      </tbody>
      <ModelCostBreakdown
        v-for="model in modelComparison"
        :key="model.model"
        :model="model"
        :label="modelLabel(model.model)"
        :show-subagents="hasSubagents"
      />
      <tbody
        v-if="hasSubagents && (subagentUsage?.subagents?.length ?? 0) > 0"
        class="token-cost-subagent-breakdown"
      >
        <tr
          v-for="sub in subagentUsage?.subagents ?? []"
          :key="sub.conversation_id"
          class="token-cost-legend-row token-cost-subagent-row"
        >
          <th scope="row">
            <a :href="`/c/${sub.slug}`" :title="`Open subagent ${sub.slug}`">{{
              sub.slug || "subagent"
            }}</a>
          </th>
          <td class="token-cost-empty" aria-label="No direct usage">—</td>
          <td class="token-cost-cell">
            <span class="token-cost-legend-tokens"
              >{{ sub.llm_calls }} {{ sub.llm_calls === 1 ? "call" : "calls" }}</span
            >
            <span
              v-if="sub.estimated_usd > 0 || sub.reported_usd > 0"
              class="token-cost-legend-cost"
              >{{ formatUsd(Math.max(sub.estimated_usd, sub.reported_usd)) }}</span
            >
            <span v-else-if="sub.unpriced_calls === 0" class="token-cost-legend-cost">{{
              formatUsd(0)
            }}</span>
            <span v-else class="token-cost-empty">no pricing</span>
          </td>
        </tr>
      </tbody>
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
import type { Message, Model } from "../../types";
import ContextLegend from "./ContextLegend.vue";
import ModelCostBreakdown from "./ModelCostBreakdown.vue";
import {
  modelCostsApi,
  subagentUsageApi,
  type ModelCostDTO,
  type SubagentUsageDTO,
} from "../../services/api";
import { findModelByName, prettyModelLabels } from "../../utils/modelNames";
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
  stackedAreaPaths,
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
import { layoutPanes, type PaneBox, type UsagePane } from "../../utils/usagePanes";
import { contextCategories, contextLayers, contextSegmentStarts } from "./contextCategories";
import { contextCompositionPoints, type Point } from "./contextComposition";

const props = defineProps<{
  entries: UsageEntry[];
  /** The conversation's messages, for the context graph. */
  messages?: Message[];
  // Model list, for turning the upstream wire names recorded in usage data
  // (e.g. "accounts/fireworks/models/glm-5p3-flash") into display names.
  models: Model[];
  otherUsageRows?: OtherUsageRow[];
  conversationId?: string | null;
  active?: boolean;
  /** Which graphs are on; they stack in a fixed order whatever this one is. */
  panes: UsagePane[];
}>();

// The legend names each model the way the picker does; the raw usage name
// stays on hover, and is what an unresolvable model shows.
const labels = computed(() => prettyModelLabels(props.models ?? []));
function modelLabel(name: string): string {
  const m = findModelByName(props.models ?? [], name);
  return (m && labels.value.get(m.id)) || name;
}

// Pixel geometry, drawn 1:1: the viewBox is as wide as the svg actually is
// (measured below), so the graph stretches with the popup instead of scaling
// its text. Each pane's label runs down the left edge, in one or two lines
// (LABEL_X); tick labels sit between it and the plot.
const DEFAULT_W = 420;
const MIN_W = 200;
const PADL = 52;
const PADR = 8;
// Room above the first pane for its label, which can be taller than the pane
// (the incremental strip is shorter than "Incremental" is long).
const PADT = 12;
const PANE_GAP = 10;
const PADB = 18;
const LABEL_X = [9, 20];

const W = ref(DEFAULT_W);
const svgRef = ref<SVGSVGElement | null>(null);
let resizeObserver: ResizeObserver | null = null;
watch(svgRef, (svg) => {
  resizeObserver?.disconnect();
  resizeObserver = null;
  if (!svg || typeof ResizeObserver === "undefined") return;
  // contentRect is layout size: unlike getBoundingClientRect it ignores the
  // popup's open animation, which scales it.
  resizeObserver = new ResizeObserver((entries) => {
    const width = Math.round(entries[entries.length - 1].contentRect.width);
    if (width > 0) W.value = Math.max(MIN_W, width);
  });
  resizeObserver.observe(svg);
});
onBeforeUnmount(() => resizeObserver?.disconnect());

const plotW = computed(() => W.value - PADL - PADR);
const paneBoxes = computed<PaneBox[]>(() => layoutPanes(props.panes, PADT, PANE_GAP));
const H = computed(() => (paneBoxes.value.at(-1)?.bottom ?? PADT) + PADB);
const showsContext = computed(() => props.panes.includes("context"));
// The context graph needs no pricing, so it need not wait for the lookup.
const needsPricing = computed(() => props.panes.some((pane) => pane !== "context"));
const pricingPending = computed(() => loading.value && needsPricing.value);

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
  (subagentUsage.value?.per_model ?? [])
    // A call that failed or was canceled before any tokens records zero
    // usage with no model; it prices nowhere and costs nothing, so it
    // creates no model section.
    .filter(
      (row) =>
        row.input_tokens + row.cache_creation_input_tokens + row.cache_read_input_tokens + row.output_tokens >
        0,
    )
    .map((row) => ({
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

const xMode = ref<"calls" | "time">("calls");

const layout = computed<XLayout>(() =>
  xMode.value === "time" ? timeXLayout(props.entries) : callXLayout(stack.value?.n ?? 0),
);

function xAt(i: number): number {
  return PADL + layout.value.xs[i] * plotW.value;
}

const unit = computed(() => (stack.value?.weighted ? "cost" : "tokens"));

// The context graph's points are one per LLM call, like the usage entries, so
// they normally share the cost graph's x positions. Should the two ever
// disagree on the number of calls, the context graph spreads its own points
// evenly instead.
const ctxPoints = computed<Point[]>(() =>
  props.messages ? contextCompositionPoints(props.messages) : [],
);
const categories = computed(() => contextCategories(ctxPoints.value));
const ctxLayers = computed(() => contextLayers(ctxPoints.value, categories.value));
const ctxAligned = computed(() => ctxPoints.value.length === (stack.value?.n ?? 0));
const ctxLayout = computed<XLayout>(() =>
  ctxAligned.value ? layout.value : callXLayout(ctxPoints.value.length),
);

// A vertical guide through every pane, skipping the gaps between them.
function guide(x: number): string {
  const px = x.toFixed(1);
  return paneBoxes.value.map((b) => `M${px},${b.top}V${b.bottom}`).join("");
}

// Compactions: new generations, and in-place records in the context. Both
// reset the context, so they are marked through every pane.
const markStarts = computed<number[]>(() => {
  const starts = new Set(generationStarts(props.entries));
  if (ctxAligned.value) for (const i of contextSegmentStarts(ctxPoints.value)) starts.add(i);
  return [...starts].sort((a, b) => a - b);
});

function markX(i: number): number {
  return PADL + generationBoundary(bars.value, layout.value.xs, i) * plotW.value;
}

// Non-overlapping per-call bars; each draws (and hovers as) one call.
const bars = computed<DeltaBar[]>(() => {
  const s = stack.value;
  if (!s || s.n === 0 || s.segments.length === 0) return [];
  return deltaBars(layout.value.xs, s.deltaLayers[s.segments.length - 1]);
});

// One path per segment holding a rect per bar with a nonzero share.
function barPaths(s: TokenCostStack, yAt: (v: number) => number): string[] {
  if (s.maxDelta === 0) return [];
  const xs = bars.value.map((b) => [
    (PADL + b.left * plotW.value).toFixed(2),
    (PADL + b.right * plotW.value).toFixed(2),
  ]);
  return s.segments.map((_, si) => {
    let d = "";
    bars.value.forEach((b, k) => {
      const top = s.deltaLayers[si][b.call];
      const bottom = si === 0 ? 0 : s.deltaLayers[si - 1][b.call];
      if (top <= bottom) return;
      const [x0, x1] = xs[k];
      const yT = yAt(top).toFixed(1);
      const yB = yAt(bottom).toFixed(1);
      d += `M${x0},${yB}V${yT}H${x1}V${yB}Z`;
    });
    return d;
  });
}

function tickLabel(v: number): string {
  if (!stack.value?.weighted) return formatTokenCount(v);
  if (v >= 10) return `$${Math.round(v)}`;
  if (v >= 1) return `$${parseFloat(v.toFixed(1))}`;
  if (v >= 0.01) return `$${parseFloat(v.toFixed(2))}`;
  return formatUsd(v);
}

interface PaneTick {
  value: number;
  y: number;
  label: string;
  /** Draw a gridline across the plot. */
  grid: boolean;
}

interface PaneView {
  id: UsagePane;
  box: PaneBox;
  /** Label lines, read bottom-to-top down the pane's left side. */
  label: string[];
  ticks: PaneTick[];
  /** One filled path per band, drawn bottom-to-top. */
  paths: string[];
  colors: string[];
}

function yScale(box: PaneBox, max: number): (v: number) => number {
  return (v) => box.top + box.height * (1 - v / (max || 1));
}

const paneViews = computed<PaneView[]>(() => {
  const s = stack.value;
  if (!s || s.n === 0) return [];
  const area = (yAt: (v: number) => number) => ({ left: PADL, right: W.value - PADR, yAt });
  return paneBoxes.value.map((box): PaneView => {
    switch (box.pane) {
      case "context": {
        const max = ctxLayers.value.at(-1)?.reduce((m, v) => Math.max(m, v), 0) ?? 0;
        const yAt = yScale(box, max);
        return {
          id: box.pane,
          box,
          label: ["Context", "tokens"],
          ticks: yTicks(max).map((value) => ({
            value,
            y: yAt(value),
            label: formatTokenCount(value),
            grid: true,
          })),
          paths: max > 0 ? stackedAreaPaths(ctxLayers.value, ctxLayout.value, area(yAt)) : [],
          colors: categories.value.map((c) => c.color),
        };
      }
      case "cumulative": {
        const yAt = yScale(box, s.maxY);
        return {
          id: box.pane,
          box,
          label: ["Cumulative", unit.value],
          ticks: yTicks(s.maxY).map((value) => ({
            value,
            y: yAt(value),
            label: tickLabel(value),
            grid: true,
          })),
          paths: s.maxY > 0 ? stackedAreaPaths(s.layers, layout.value, area(yAt)) : [],
          colors: s.segments.map((seg) => seg.color),
        };
      }
      case "incremental": {
        // Too short for gridlines: just the tallest call's value, by the top.
        const yAt = yScale(box, s.maxDelta);
        return {
          id: box.pane,
          box,
          label: ["Incremental", unit.value],
          ticks:
            s.maxDelta > 0
              ? [{ value: s.maxDelta, y: box.top + 3, label: tickLabel(s.maxDelta), grid: false }]
              : [],
          paths: barPaths(s, yAt),
          colors: s.segments.map((seg) => seg.color),
        };
      }
    }
  });
});

const xAxisLabel = computed(() => {
  const s = stack.value;
  if (!s) return "";
  if (xMode.value === "calls") return "LLM call";
  const lay = layout.value;
  const dur = lay.activeMs > 0 ? `${formatDuration(lay.activeMs)} active` : "time";
  return lay.turns.length > 1 ? `${dur} · gaps = idle between turns` : dur;
});

const hintText = computed(() => {
  const parts: string[] = [];
  if (markStarts.value.length) parts.push("Dashed lines mark compactions.");
  if (cacheMissMarkers.value.length > 0)
    parts.push(
      "Red dashed lines mark mid-generation model switches (the cache was rebuilt); grey ones mark other mid-generation cache misses.",
    );
  return parts.join(" ") || "";
});

// Mid-generation cache-miss markers: red when the model changed (the user's
// action rebuilds the context and loses the cache), light grey for any other
// mid-generation cache miss — a turn whose cache read is < 20% of the prior
// turn's total input. Cross-generation misses are expected after compactions
// and get no line.
const cacheMissMarkers = computed<{ index: number; kind: "model" | "miss" }[]>(() => {
  const entries = props.entries;
  const out: { kind: "model" | "miss"; index: number }[] = [];
  for (let i = 1; i < entries.length; i++) {
    const prev = entries[i - 1];
    const cur = entries[i];
    if (prev.generation !== undefined && cur.generation !== undefined && prev.generation !== cur.generation)
      continue;
    if (cur.model && prev.model && cur.model !== prev.model) out.push({ kind: "model", index: i });
    else if (
      (cur.cache_read_input_tokens || 0) <
      0.2 *
        ((prev.input_tokens || 0) +
          (prev.cache_creation_input_tokens || 0) +
          (prev.cache_read_input_tokens || 0))
    ) {
      out.push({ kind: "miss", index: i });
    }
  }
  return out;
});

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
  const px = ((ev.clientX - rect.left) / rect.width) * W.value;
  hoverFrac.value = (px - PADL) / plotW.value;
}

function amount(v: number): string {
  return stack.value?.weighted ? formatUsd(v) : `${formatTokenCount(v)} tok`;
}

// The context point under the pointer: the hovered call's own, or, when the
// two series disagree on how many calls there are, the nearest by position.
const ctxHoverIndex = computed<number | null>(() => {
  const points = ctxPoints.value;
  if (points.length === 0) return null;
  if (ctxAligned.value) return hoverIndex.value;
  const frac = hoverFrac.value;
  if (frac === null) return null;
  const xs = ctxLayout.value.xs;
  let best = 0;
  for (let i = 1; i < xs.length; i++) {
    if (Math.abs(xs[i] - frac) < Math.abs(xs[best] - frac)) best = i;
  }
  return best;
});

// What the context legend counts: the hovered call, else the latest.
const legendPoint = computed<Point | null>(() => {
  const i = ctxHoverIndex.value;
  return ctxPoints.value[i ?? ctxPoints.value.length - 1] ?? null;
});

// "call 169, $1.61, cumulative $47.05, context 174k", then the snippet:
// "(bash)". Each graph that is on adds its figure. Time mode adds the time.
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
  if (props.panes.includes("incremental")) {
    // A call to an unpriced model adds nothing to a dollar graph, but it is
    // not free: say so, with the provider's own figure when there is one.
    if (s.weighted && !(e.model && costs.value[e.model])) {
      parts.push(e.cost_usd > 0 ? `${formatUsd(e.cost_usd)} reported` : "unpriced");
    } else {
      parts.push(amount(s.deltaLayers[top][i]));
    }
  }
  if (props.panes.includes("cumulative")) parts.push(`cumulative ${amount(s.layers[top][i])}`);
  const point = props.panes.includes("context") ? legendPoint.value : null;
  if (point && ctxHoverIndex.value !== null) parts.push(`context ${formatTokenCount(point.total)}`);
  return parts.join(", ");
});

const hoverSnippet = computed(() =>
  hoverIndex.value === null ? "" : props.entries[hoverIndex.value]?.snippet || "",
);
</script>
