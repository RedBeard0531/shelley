<!-- Stacked cumulative token-cost graph shown in the context usage popup.
     X axis: LLM calls or wall-clock time (toggle); in time mode, idle time
     between turns is collapsed into fixed-width gaps. Y axis: cumulative
     dollars per (model, token-band) segment — every segment has its own color
     — falling back to raw token counts when no model in the conversation has
     known pricing.

     Subagent cost and "other" (indirect) LLM usage — compaction
     summarization, LLM-backed tools, slug generation, … — are not part of the
     graph. Other-usage rows arrive via the otherUsageRows prop (aggregated
     client-side from message other_usage_data) and subagent cost is fetched
     separately. Each scope has its own per-model breakdown and subtotal;
     both are included in the combined total. -->
<template>
  <div class="token-cost-graph">
    <div v-if="loading" class="token-cost-graph-note">Loading pricing…</div>
    <div v-else-if="stack && stack.n > 0" class="token-cost-timeline">
      <div v-if="hasSubagents" class="token-cost-graph-note">
        Main conversation · cumulative direct {{ stack.weighted ? "cost" : "tokens" }}
      </div>
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
        @mouseleave="hoverIndex = null"
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
        <path v-for="(d, s) in segPaths" :key="s" :d="d" :fill="stack.segments[s].color" />
        <line
          v-for="i in genStarts"
          :key="`gen-${i}`"
          :x1="xAt(i)"
          :y1="PADT"
          :x2="xAt(i)"
          :y2="H - PADB"
          class="token-cost-gen-line"
        />
        <line :x1="PADL" :y1="PADT" :x2="PADL" :y2="H - PADB" class="token-cost-axis" />
        <line :x1="PADL" :y1="H - PADB" :x2="W - PADR" :y2="H - PADB" class="token-cost-axis" />
        <line
          v-if="hoverIndex !== null && stack.n > 1"
          :x1="xAt(hoverIndex)"
          :y1="PADT"
          :x2="xAt(hoverIndex)"
          :y2="H - PADB"
          class="token-cost-hover-line"
        />
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
        <text :x="(PADL + W - PADR) / 2" :y="H - 4" text-anchor="middle" class="token-cost-label">
          {{ xAxisLabel }}
        </text>
      </svg>
      <div class="token-cost-hover-readout">
        <template v-if="hoverEntry">
          <div>
            call {{ hoverIndex! + 1 }} of {{ stack.n
            }}<template v-if="hoverGeneration"> (gen {{ hoverGeneration }})</template
            ><template v-if="hoverTime"> · {{ hoverTime }}</template> · cumulative {{ hoverTotal }}
          </div>
          <div v-if="hoverEntry.snippet" class="token-cost-hover-snippet">
            {{ hoverEntry.snippet }}
          </div>
        </template>
        <template v-else>
          <div>{{ hintText }}</div>
        </template>
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
    <div
      v-if="!loading && (stack || otherBreakdown || hasSubagents)"
      class="token-cost-columns"
      :class="{ 'token-cost-columns-with-subagents': hasSubagents }"
    >
      <section class="token-cost-column" aria-label="Main conversation">
        <h3 class="token-cost-column-heading">Main conversation</h3>
        <div class="token-cost-legend">
          <ModelCostBreakdown
            v-for="mu in stack?.perModel || []"
            :key="mu.model"
            :usage="mu"
            :hide-zero-cache-write="subscriptionOnly.get(mu.model) === true"
            show-colors
          />
          <template v-if="otherBreakdown && otherBreakdown.perPurpose.length > 0">
            <div class="token-cost-model-row">
              <span class="token-cost-model-name">Other (indirect)</span>
              <span
                v-if="otherKnownUsd > 0 || otherBreakdown.totals.unpricedCalls === 0"
                class="token-cost-legend-cost"
                >{{ formatUsd(otherKnownUsd) }}</span
              >
              <span v-else class="token-cost-legend-unit">no pricing</span>
            </div>
            <div
              v-for="p in otherBreakdown.perPurpose"
              :key="p.purpose"
              class="token-cost-legend-row"
            >
              <span class="token-cost-legend-label">{{ p.purpose }}</span>
              <span class="token-cost-legend-tokens"
                >{{ p.llmCalls }} {{ p.llmCalls === 1 ? "call" : "calls" }}</span
              >
              <span class="token-cost-legend-tokens">{{ formatTokenCount(p.tokens) }}</span>
              <span v-if="otherPurposeKnownUsd(p) > 0 || p.priced" class="token-cost-legend-cost">{{
                formatUsd(otherPurposeKnownUsd(p))
              }}</span>
              <span v-else class="token-cost-legend-unit">no pricing</span>
            </div>
          </template>
        </div>
        <div
          class="token-cost-model-row token-cost-subtotal-row"
          data-testid="conversation-cost-subtotal"
        >
          <span class="token-cost-model-name">Subtotal</span>
          <span v-if="mainKnownUsd > 0 || mainUnpricedCalls === 0" class="token-cost-legend-cost">{{
            formatUsd(mainKnownUsd)
          }}</span>
          <span v-else class="token-cost-legend-unit">no pricing</span>
        </div>
      </section>
      <section v-if="hasSubagents" class="token-cost-column" aria-label="Sub-agents">
        <h3 class="token-cost-column-heading">Sub-agents</h3>
        <div class="token-cost-legend">
          <ModelCostBreakdown
            v-for="row in subagentModels"
            :key="JSON.stringify([row.usage.model, row.url])"
            :usage="row.usage"
            :source="row.url"
            :show-source="row.showSource"
            :hide-zero-cache-write="isSubscriptionUsage(row.usage.model, row.url)"
          />
        </div>
        <div class="token-cost-graph-note">Includes nested sub-agents and indirect usage.</div>
        <div class="token-cost-model-row token-cost-subtotal-row" data-testid="subagent-cost-row">
          <span class="token-cost-model-name">Subtotal</span>
          <span
            v-if="subagentKnownUsd > 0 || subagentUsage?.unpriced_calls === 0"
            class="token-cost-legend-cost"
            >{{ formatUsd(subagentKnownUsd) }}</span
          >
          <span v-else class="token-cost-legend-unit">no pricing</span>
        </div>
      </section>
    </div>
    <div
      v-if="!loading && !subagentLoading && showCostSummary"
      class="token-cost-model-row token-cost-total-row"
      data-testid="token-cost-total"
    >
      <span class="token-cost-model-name">Total</span>
      <span class="token-cost-legend-cost">≈{{ formatUsd(costSummary.totalUsd) }}</span>
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
  buildOtherUsageBreakdown,
  buildTokenCostStack,
  callXLayout,
  countConfirmedUnpricedCalls,
  formatDuration,
  formatTokenCount,
  formatUsd,
  generationStarts,
  timeXLayout,
  yTicks,
  TOKEN_BANDS,
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

const W = 280;
const H = 150;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;

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
const subagentModels = computed(() => {
  const rows = subagentUsage.value?.per_model ?? [];
  const modelCounts = new Map<string, number>();
  for (const row of rows) modelCounts.set(row.model, (modelCounts.get(row.model) ?? 0) + 1);
  return rows.map((row) => ({
    url: row.url,
    showSource: modelCounts.get(row.model)! > 1,
    usage: {
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
    } satisfies ModelUsage,
  }));
});

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
const plotH = H - PADT - PADB;

const xMode = ref<"calls" | "time">("calls");

const layout = computed<XLayout>(() =>
  xMode.value === "time" ? timeXLayout(props.entries) : callXLayout(stack.value?.n ?? 0),
);

function xAt(i: number): number {
  return PADL + layout.value.xs[i] * plotW;
}

function yAt(v: number): number {
  const maxY = stack.value?.maxY || 1;
  return PADT + plotH * (1 - v / maxY);
}

// One path per (model, band) segment; each turn is a separate subpath so
// idle time between turns renders as a gap in time mode. Zero-width turns
// (single call, or all calls sharing one second-granularity timestamp)
// become narrow slabs so they stay visible.
const segPaths = computed<string[]>(() => {
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
  if (xMode.value === "calls") return `LLM call number (${s.n} calls)`;
  const lay = layout.value;
  const dur = lay.activeMs > 0 ? `${formatDuration(lay.activeMs)} active` : "time";
  return lay.turns.length > 1 ? `${dur} · gaps = idle between turns` : dur;
});

const hintText = computed(() => {
  const parts: string[] = [];
  if (genStarts.value.length) parts.push("Dashed lines mark new generations (compactions).");
  if (xMode.value === "time" && layout.value.turns.length > 1)
    parts.push("Idle time between turns is not to scale.");
  return parts.join(" ");
});

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

function isSubscriptionUsage(name: string, url?: string): boolean {
  const sources = props.models.filter(
    (model) =>
      model.api_model_name === name &&
      model.base_url &&
      (url === model.base_url || url?.startsWith(`${model.base_url}/`)),
  );
  return sources.length > 0 && sources.every((model) => model.mode === "chatgpt");
}

const hoverIndex = ref<number | null>(null);

// A shrinking entries list (e.g. switching conversations) could leave a stale
// out-of-range index behind.
watch(
  () => stack.value?.n,
  () => (hoverIndex.value = null),
);

function onMove(ev: MouseEvent) {
  const s = stack.value;
  if (!s || s.n === 0) return;
  const svg = ev.currentTarget as SVGSVGElement;
  const rect = svg.getBoundingClientRect();
  const px = ((ev.clientX - rect.left) / rect.width) * W;
  const frac = (px - PADL) / plotW;
  const xs = layout.value.xs;
  let best = 0;
  let bestD = Infinity;
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - frac);
    if (d < bestD) {
      bestD = d;
      best = i;
    }
  }
  hoverIndex.value = best;
}

const hoverEntry = computed<UsageEntry | null>(() => {
  const s = stack.value;
  const i = hoverIndex.value;
  if (!s || i === null || i >= s.n) return null;
  return props.entries[i];
});

const genStarts = computed(() => generationStarts(props.entries));

// Generation number shown in the hover readout, only when the conversation
// actually spans multiple generations.
const hoverGeneration = computed<number | null>(() => {
  if (genStarts.value.length === 0) return null;
  return hoverEntry.value?.generation ?? null;
});

// Wall-clock time of the hovered call, shown in time mode.
const hoverTime = computed(() => {
  const ts = hoverEntry.value?.timestamp;
  if (xMode.value !== "time" || !ts) return "";
  return new Date(ts).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
});

const hoverTotal = computed(() => {
  const s = stack.value;
  const i = hoverIndex.value;
  if (!s || i === null || i >= s.n || s.segments.length === 0) return "";
  const top = s.layers[s.segments.length - 1][i];
  return s.weighted ? formatUsd(top) : `${formatTokenCount(top)} tok`;
});
</script>
