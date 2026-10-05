<!-- Legend for the context graph: what each color is and how many tokens it
     holds at one call (the hovered one, else the latest), split for tool
     categories into args (recorded tool inputs) vs output (tool results),
     with each row's share of the call's context. Hovering a row names what
     the category is made of. -->
<template>
  <div
    v-if="breakdownRows.length > 0"
    class="context-legend context-breakdown"
    role="table"
    aria-label="Estimated context mix"
  >
    <div class="context-breakdown-row context-breakdown-head" role="row">
      <span class="context-breakdown-label" role="columnheader">category</span>
      <span class="context-breakdown-tokens" role="columnheader">args</span>
      <span class="context-breakdown-tokens" role="columnheader">output</span>
      <span class="context-breakdown-total" role="columnheader">total</span>
      <span class="context-breakdown-pct" role="columnheader" />
    </div>
    <div
      v-for="row in breakdownRows"
      :key="row.key"
      v-tooltip.top="row.hint"
      role="row"
      :aria-label="`${row.label}: ${row.hint}`"
      class="context-breakdown-row"
    >
      <span class="context-breakdown-label" role="cell">
        <i :style="{ background: row.color }" />{{ row.label }}
      </span>
      <span class="context-breakdown-tokens" role="cell">
        <TokenCount v-if="row.args !== null" :tokens="row.args" />
      </span>
      <span class="context-breakdown-tokens" role="cell">
        <TokenCount v-if="row.output !== null" :tokens="row.output" />
      </span>
      <span class="context-breakdown-total" role="cell">
        <TokenCount :tokens="row.total" />
      </span>
      <span class="context-breakdown-pct" role="cell">
        {{ breakdownTotal > 0 ? ((row.total / breakdownTotal) * 100).toFixed(0) + "%" : "" }}
      </span>
    </div>
    <div class="context-breakdown-row context-breakdown-total-row" role="row">
      <span class="context-breakdown-label" role="cell">total</span>
      <span class="context-breakdown-tokens" role="cell">
        <TokenCount v-if="breakdownArgsTotal" :tokens="breakdownArgsTotal" />
      </span>
      <span class="context-breakdown-tokens" role="cell">
        <TokenCount v-if="breakdownOutputTotal" :tokens="breakdownOutputTotal" />
      </span>
      <span class="context-breakdown-total" role="cell">
        <TokenCount :tokens="breakdownTotal" />
      </span>
      <span class="context-breakdown-pct" role="cell">100%</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from "vue";
import { formatTokenCount } from "../../utils/tokenCostGraph";
import { categoryHint, categoryTokens, type ContextCategory } from "./contextCategories";
import { isToolCategory, type Point } from "./contextComposition";

const props = defineProps<{ categories: ContextCategory[]; point: Point }>();

/** Bold-italic unit suffix ("3.0k" → 3.0<i>k</i>), as in the cost table. */
const TokenCount = (props: { tokens: number }) => {
  const text = formatTokenCount(props.tokens);
  const match = text.match(/^([\d,.]+)([kMB]?)$/)!;
  return match[2]
    ? [match[1], h("i", { class: "context-breakdown-unit" }, match[2])]
    : text;
};
TokenCount.props = ["tokens"];

// One row per category present at this call. Tool categories split their
// tokens into args (recorded tool inputs) and output (tool results); text
// and images are all "output" of their own kind, shown in the total column
// only.
const breakdownRows = computed(() =>
  props.categories
    .map((category) => {
      const total = categoryTokens(props.point, category.key);
      if (total === 0) return null;
      const tool = isToolCategory(category.key);
      const args = tool ? (props.point.args[category.key] || 0) : null;
      return {
        ...category,
        args: tool ? args : null,
        output: tool ? total - (args || 0) : null,
        total,
        hint: categoryHint(category.key, props.point),
      };
    })
    .filter((row): row is NonNullable<typeof row> => row !== null),
);
const breakdownTotal = computed(() => breakdownRows.value.reduce((sum, row) => sum + row.total, 0));
const breakdownArgsTotal = computed(() =>
  breakdownRows.value.reduce((sum, row) => sum + (row.args || 0), 0),
);
const breakdownOutputTotal = computed(() =>
  breakdownRows.value.reduce((sum, row) => sum + (row.output || 0), 0),
);
</script>
