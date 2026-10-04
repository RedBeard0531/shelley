<!-- Reconstructed context composition per LLM call. The top line is the
     provider-reported context size; colored stacked areas estimate which
     visible history categories made it up. -->
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
      <div
        class="token-cost-legend context-composition-legend"
        role="list"
        aria-label="Estimated context composition"
      >
        <div
          v-for="category in categories"
          :key="category.key"
          class="context-composition-legend-item"
          role="listitem"
        >
          <button
            type="button"
            class="token-cost-legend-row context-composition-legend-row"
            v-tooltip.focus.top="categoryHint(category.key, legendPoint)"
            :aria-label="`${category.label}: ${formatTokenCount(categoryTokens(legendPoint, category.key))}. ${categoryHint(category.key, legendPoint)}`"
            @mouseenter="showLegendTooltipOnHover"
            @mouseleave="hideLegendTooltipOnHover"
          >
            <span class="token-cost-chip" :style="{ background: category.color }" aria-hidden="true" />
            <span class="token-cost-legend-label context-composition-legend-label">{{ category.label }}</span>
            <span class="token-cost-legend-tokens context-composition-legend-tokens">{{
              formatTokenCount(categoryTokens(legendPoint, category.key))
            }}</span>
          </button>
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
import { computed, ref } from "vue";
import type { Message } from "../../types";
import { contextCompositionPoints, type Point } from "./contextComposition";
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

type Category = { key: string; label: string; color: string };

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

const CATEGORY_LABELS: Record<string, string> = {
  text: "text",
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
  text: "hsl(174 58% 48%)",
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

const points = computed<Point[]>(() => contextCompositionPoints(props.messages));

const categories = computed<Category[]>(() => {
  const keys = new Set<string>();
  for (const point of points.value) {
    for (const key of Object.keys(point.parts)) keys.add(key);
  }
  return [
    ...(["user", "assistant", "reasoning"].some((key) => keys.has(key))
      ? [{ key: "text", label: CATEGORY_LABELS.text, color: CATEGORY_COLORS.text }]
      : []),
    ...BASH_CATEGORIES.filter((key) => key !== "bash:other" && keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
    })),
    ...TOOL_CATEGORIES.slice(0, 2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
    })),
    ...(keys.has("bash:other")
      ? [{ key: "bash:other", label: CATEGORY_LABELS["bash:other"], color: CATEGORY_COLORS["bash:other"] }]
      : []),
    ...TOOL_CATEGORIES.slice(2).filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
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
    index > 0 && point.segment !== points.value[index - 1].segment ? [index] : [],
  ),
);

const hoverIndex = ref<number | null>(null);
const hoverX = ref<number | null>(null);
const hoverPoint = computed(() =>
  hoverIndex.value === null ? null : points.value[hoverIndex.value] || null,
);
const legendPoint = computed(() => hoverPoint.value || points.value.at(-1)!);

function dispatchTooltipFocus(target: EventTarget | null, type: "focus" | "blur") {
  if (target instanceof HTMLElement) target.dispatchEvent(new FocusEvent(type));
}

function showLegendTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "focus");
}

function hideLegendTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "blur");
}

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

function categoryTokens(point: Point, key: string) {
  if (key === "text") return ["user", "assistant", "reasoning"].reduce((sum, part) => sum + (point.parts[part] || 0), 0);
  return point.parts[key] || 0;
}

function plottedTotal(point: Point) {
  return categories.value.reduce((sum, category) => sum + categoryTokens(point, category.key), 0);
}

function categoryHint(key: string, point: Point) {
  if (key === "text") {
    return ["user", "assistant", "reasoning"].map((part) => `${part} ${formatTokenCount(point.parts[part] || 0)}`).join(" · ");
  }
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
