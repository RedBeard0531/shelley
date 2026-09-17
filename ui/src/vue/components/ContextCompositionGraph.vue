<!-- Reconstructed context composition per LLM call. The top line is the
     provider-reported context size; colored stacked areas estimate which
     visible history categories made it up. Each message's bytes/4 estimate
     is added once when it enters the context and never rescaled; fixed
     request overhead (tool definitions) folds into the system band at each
     generation's first call, and the per-point residue is its own
     "request overhead" band. -->
<template>
  <div class="context-composition-graph">
    <template v-if="points.length > 0">
      <div class="token-cost-controls context-composition-graph-header">
        <span>estimated composition</span>
        <span class="token-cost-controls-spacer" />
        <slot name="mode-controls" />
      </div>
      <svg
        :viewBox="`0 0 ${W} ${H}`"
        class="context-composition-graph-svg"
        role="img"
        :aria-label="`Context composition across ${points.length} LLM calls`"
        @mousemove="onMove"
        @mouseleave="clearHover"
      >
        <path
          v-for="(category, index) in categories"
          :key="category.key"
          :d="areaPath(index)"
          :fill="category.color"
          class="context-composition-area"
        />
        <line
          v-for="index in compactionStarts"
          :key="`compaction-${index}`"
          :x1="xAt(index)"
          :y1="PADT"
          :x2="xAt(index)"
          :y2="H - PADB"
          class="token-cost-gen-line"
        />
        <line
          v-for="index in modelChangePoints"
          :key="`model-change-${index}`"
          :x1="xAt(index)"
          :y1="PADT"
          :x2="xAt(index)"
          :y2="H - PADB"
          class="context-composition-model-line"
        />
        <line :x1="PADL" :y1="H - PADB" :x2="W - PADR" :y2="H - PADB" class="token-cost-axis" />
        <line
          v-if="hoverX !== null"
          :x1="hoverX"
          :y1="PADT"
          :x2="hoverX"
          :y2="H - PADB"
          class="token-cost-hover-line"
        />
        <text
          v-for="tick in yTicks"
          :key="tick"
          :x="PADL - 4"
          :y="yAtTokens(tick) + 3"
          text-anchor="end"
          class="token-cost-label"
        >{{ formatTokenCount(tick) }}</text>
        <text :x="PADL" :y="H - 4" class="token-cost-label">1</text>
        <text :x="W - PADR" :y="H - 4" text-anchor="end" class="token-cost-label">{{ points.length }}</text>
      </svg>
      <div
        class="token-cost-hover-readout context-composition-readout"
      >
        <template v-if="hoverPoint">
          call {{ hoverIndex! + 1 }} of {{ points.length }} ·
          <b>{{ formatTokenCount(hoverPoint.total) }}</b> tokens
        </template>
        <template v-else>
          current <b>{{ formatTokenCount(points.at(-1)!.total) }}</b> tokens
        </template>
      </div>
      <div class="context-composition-legend" role="table" aria-label="Estimated context composition">
        <div class="context-composition-legend-row context-composition-legend-head">
          <span class="context-composition-legend-label">category</span>
          <span class="context-composition-legend-tokens">args</span>
          <span class="context-composition-legend-tokens">output</span>
          <span class="context-composition-legend-total">total</span>
          <span class="context-composition-legend-pct" />
        </div>
        <div
          v-for="row in breakdownRows"
          :key="row.key"
          v-tooltip.top="row.hint"
          role="row"
          :aria-label="`${row.label}: ${row.hint}`"
          class="context-composition-legend-row"
        >
          <span class="context-composition-legend-label">
            <i :style="{ background: row.color }" />{{ row.label }}
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="row.args !== null" :tokens="row.args" />
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="row.output !== null" :tokens="row.output" />
          </span>
          <span class="context-composition-legend-total">
            <TokenCount :tokens="row.total" />
          </span>
          <span class="context-composition-legend-pct">
            {{ breakdownTotal > 0 ? ((row.total / breakdownTotal) * 100).toFixed(0) + "%" : "" }}
          </span>
        </div>
        <div class="context-composition-legend-row context-composition-total-row">
          <span class="context-composition-legend-label">total</span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="breakdownArgsTotal" :tokens="breakdownArgsTotal" />
          </span>
          <span class="context-composition-legend-tokens">
            <TokenCount v-if="breakdownOutputTotal" :tokens="breakdownOutputTotal" />
          </span>
          <span class="context-composition-legend-total">
            <TokenCount :tokens="breakdownTotal" />
          </span>
          <span class="context-composition-legend-pct">100%</span>
        </div>
      </div>
      <div v-if="compactionStarts.length" class="context-composition-compaction-note">
        Dashed lines mark compactions.
      </div>
    </template>
    <div v-else class="token-cost-hover-readout context-composition-readout">No context data yet.</div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, ref, watch } from "vue";
import type { Message } from "../../types";
import { buildCompositionPoints, type CompositionPoint } from "../../utils/contextComposition";
import { formatTokenCount } from "../../utils/tokenCostGraph";

const props = defineProps<{ messages: Message[] }>();

const W = 280;
const H = 150;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;
const plotWidth = W - PADL - PADR;
const plotHeight = H - PADT - PADB;

type Category = { key: string; label: string; color: string; isTool?: boolean };
const BASH_CATEGORIES = [
  "bash:code search",
  "bash:file read",
  "bash:build/test",
  "bash:script/query",
  "bash:system",
  "bash:other",
] as const;

const TOOL_CATEGORIES = [
  "repo/read",
  "repo/edit",
  "tool:browser/web",
  "tool:other",
] as const;

// Displayed as separate bands rather than one "text" lump.
const TEXT_CATEGORIES = ["system", "user", "assistant", "reasoning"] as const;

const CATEGORY_LABELS: Record<string, string> = {
  system: "system",
  user: "user",
  assistant: "assistant",
  reasoning: "reasoning",
  overhead: "request overhead",
  images: "images",
  "bash:code search": "bash · code search",
  "bash:file read": "bash · file read",
  "bash:build/test": "bash · build/test",
  "bash:script/query": "bash · script/query",
  "bash:system": "bash · system",
  "repo/read": "repo/read",
  "repo/edit": "repo/edit",
  "bash:other": "bash · general",
  "tool:browser/web": "browser/web",
  "tool:other": "other tools",
};

const CATEGORY_COLORS: Record<string, string> = {
  // Cost graph-adjacent blue, purple, teal, and orange hues, with spaced
  // shades for neighboring context bands.
  system: "hsl(210 30% 55%)",
  user: "hsl(160 64% 48%)",
  assistant: "hsl(140 55% 45%)",
  reasoning: "hsl(184 60% 44%)",
  overhead: "hsl(220 10% 60%)",
  images: "hsl(325 65% 58%)",
  "bash:code search": "hsl(199 92% 56%)",
  "bash:file read": "hsl(199 68% 66%)",
  "bash:build/test": "hsl(234 75% 59%)",
  "bash:script/query": "hsl(350 66% 56%)",
  "bash:system": "hsl(27 96% 57%)",
  "repo/read": "hsl(190 55% 50%)",
  "repo/edit": "hsl(45 80% 54%)",
  "bash:other": "hsl(0 0% 54%)",
  "tool:browser/web": "hsl(270 58% 56%)",
  "tool:other": "hsl(213 15% 53%)",
};

const points = computed<CompositionPoint[]>(() => buildCompositionPoints(props.messages));
const categories = computed<Category[]>(() => {
  const keys = new Set<string>();
  for (const point of points.value) {
    for (const key of Object.keys(point.parts)) keys.add(key);
  }
  return [
    ...TEXT_CATEGORIES.filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
    })),
    ...(keys.has("overhead")
      ? [
          {
            key: "overhead",
            label: CATEGORY_LABELS.overhead,
            color: CATEGORY_COLORS.overhead,
          },
        ]
      : []),
    ...(keys.has("images")
      ? [{ key: "images", label: CATEGORY_LABELS.images, color: CATEGORY_COLORS.images }]
      : []),
    ...BASH_CATEGORIES.filter((key) => key !== "bash:other" && keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
    ...TOOL_CATEGORIES.slice(0, 2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
    ...(keys.has("bash:other")
      ? [{ key: "bash:other", label: CATEGORY_LABELS["bash:other"], color: CATEGORY_COLORS["bash:other"], isTool: true }]
      : []),
    ...TOOL_CATEGORIES.slice(2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      isTool: true,
    })),
  ];
});

const chartMax = computed(() => Math.max(0, ...points.value.map(plottedTotal)));
const yTicks = computed(() => {
  const max = chartMax.value;
  return max > 0 ? [...new Set([0, Math.round(max / 2), max])] : [0];
});
const compactionStarts = computed(() =>
  points.value.flatMap((point, index) =>
    index > 0 && point.generation !== points.value[index - 1].generation ? [index] : [],
  ),
);

// Red vertical lines at mid-generation model changes: switching the model
// rebuilds the context, so the user's action causes a cache miss there.
const modelChangePoints = computed<Set<number>>(() => {
  const set = new Set<number>();
  for (let i = 1; i < points.value.length; i++) {
    if (points.value[i].generation !== points.value[i - 1].generation) continue;
    if (
      points.value[i].model &&
      points.value[i - 1].model &&
      points.value[i].model !== points.value[i - 1].model
    )
      set.add(i);
  }
  return set;
});

const hoverIndex = ref<number | null>(null);
const hoverX = ref<number | null>(null);
const hoverPoint = computed(() =>
  hoverIndex.value === null ? null : points.value[hoverIndex.value] || null,
);

/** Rendered token count with a bold+italic unit suffix (k/M/B) in the
 *  breakdown table. */
const TokenCount = (props: { tokens: number }) => {
  const text = formatTokenCount(props.tokens);
  const match = text.match(/^([\d,.]+)([kMB]?)$/)!;
  return match[2]
    ? [match[1], h("i", { class: "context-composition-unit" }, match[2])]
    : text;
};
TokenCount.props = ["tokens"];

// The breakdown table reflects the hovered call when hovering the graph,
// otherwise the current (last) point.
const lastPoint = computed(() => points.value.at(-1) || null);
const breakdownPoint = computed(() => hoverPoint.value || lastPoint.value);
const breakdownRows = computed(() => {
  const point = breakdownPoint.value;
  if (!point) return [];
  return categories.value.map((category) => {
    const total = point.parts[category.key] || 0;
    const args = point.args[category.key] || 0;
    return {
      ...category,
      args: category.isTool ? args : null,
      output: category.isTool ? total - args : null,
      total,
      hint: CATEGORY_HINTS[category.key] || categoryHint(category.key, point),
    };
  });
});
const breakdownTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + row.total, 0));
const breakdownArgsTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + (row.args || 0), 0));
const breakdownOutputTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + (row.output || 0), 0));

// A shrinking or replaced message list (conversation switch) invalidates the
// stale hover point.
watch(
  () => points.value.length,
  () => {
    hoverIndex.value = null;
    hoverX.value = null;
  },
);

function areaPath(categoryIndex: number) {
  if (points.value.length === 0) return "";
  const upper = points.value.map((point, index) => {
    const sum = categories.value
      .slice(0, categoryIndex + 1)
      .reduce((total, category) => total + categoryTokens(point, category.key), 0);
    return `${xAt(index)},${yAtTokens(sum)}`;
  });
  const lower = points.value
    .map((point, index) => {
      const sum = categories.value
        .slice(0, categoryIndex)
        .reduce((total, category) => total + categoryTokens(point, category.key), 0);
      return `${xAt(index)},${yAtTokens(sum)}`;
    })
    .reverse();
  return `M${upper.join(" L")} L${lower.join(" L")} Z`;
}

function xAt(index: number) {
  if (points.value.length <= 1) return PADL + plotWidth / 2;
  return PADL + (index / (points.value.length - 1)) * plotWidth;
}

function yAtTokens(tokens: number) {
  const max = chartMax.value;
  return max > 0 ? PADT + (1 - tokens / max) * plotHeight : H - PADB;
}

function onMove(event: MouseEvent) {
  if (points.value.length === 0) return;
  const rect = (event.currentTarget as SVGElement).getBoundingClientRect();
  const pointerX = ((event.clientX - rect.left) / rect.width) * W;
  hoverX.value = Math.min(W - PADR, Math.max(PADL, pointerX));
  let nearest = 0;
  let distance = Infinity;
  for (let index = 0; index < points.value.length; index++) {
    const nextDistance = Math.abs(xAt(index) - hoverX.value);
    if (nextDistance < distance) {
      nearest = index;
      distance = nextDistance;
    }
  }
  hoverIndex.value = nearest;
}

function clearHover() {
  hoverX.value = null;
  hoverIndex.value = null;
}

function categoryTokens(point: CompositionPoint, key: string) {
  return point.parts[key] || 0;
}

function plottedTotal(point: CompositionPoint) {
  return categories.value.reduce((sum, category) => sum + categoryTokens(point, category.key), 0);
}
const CATEGORY_HINTS: Record<string, string> = {
  system:
    "System prompt and tool definitions: the byte-estimated prompt plus the fixed request overhead measured once at the generation's first call",
  user: "Text typed by the user, plus mid-conversation injections (e.g. subagent-done pokes)",
  assistant: "Assistant text output",
  reasoning:
    "Assistant internal reasoning the request carries: the provider's reported counts where it replays its reasoning, thinking text otherwise",
  overhead:
    "Per-message framing, chars/4 tokenizer drift, and cache-page quantization: the per-point residue the reconstruction doesn't explain",
  images: "Image content in messages or tool results; the server strips image bytes, so each image carries only a rough size allowance",
};

function categoryHint(key: string, point: CompositionPoint) {
  if (CATEGORY_HINTS[key]) return CATEGORY_HINTS[key];
  const breakdown = point.toolBreakdown[key];
  if (!breakdown) return "Tool output";
  const details = Object.entries(breakdown)
    .filter(([, tokens]) => tokens > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, tokens]) => `${name} ${formatTokenCount(tokens)}`)
    .join(" · ");
  return details || "Tool output";
}
</script>
