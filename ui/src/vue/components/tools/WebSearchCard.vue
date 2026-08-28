<!-- Tool card for the client-side WebSearch tool (search backed by a hosted
     web-search endpoint). Renders structured per-result rows: clickable title
     link, URL, published-age chip, and an individually expandable snippet
     (hidden by default). Reuses the native .web-search-* styles; may coexist
     with WebSearchTool.vue, which renders provider-native (server-side) search
     results arriving as web_search_tool_result / web_search_result content
     types. Vendor-neutral: no backend name surfaces here. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔍</span>
        <span class="tool-command"
          >Web Search{{ query ? ": " : "" }}<span v-if="query" class="web-search-query">{{
            query
          }}</span></span
        >
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <span v-else-if="isComplete && showCount" class="tool-success">
          {{ resultCount }} result{{ resultCount !== 1 ? "s" : "" }}
        </span>
        <span v-if="executionTime && isComplete" class="tool-time">{{ executionTime }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <div v-if="isRunning" class="tool-section">
        <div class="tool-label">Status:</div>
        <div class="tool-running-text">searching...</div>
      </div>

      <div v-else-if="hasError" class="tool-section">
        <div class="tool-label">Error:</div>
        <div class="tool-code error">{{ errorText }}</div>
      </div>

      <div v-else-if="results.length === 0" class="tool-section">
        <div class="tool-label">{{ emptyMessage }}</div>
      </div>

      <div v-else class="web-search-results">
        <div v-for="(result, index) in results" :key="index" class="web-search-result">
          <a
            :href="result.URL || ''"
            target="_blank"
            rel="noopener noreferrer"
            class="web-search-result-title"
          >
            {{ result.Title || result.URL }}
          </a>
          <div class="web-search-result-meta">
            <span class="web-search-result-url">{{ result.URL }}</span>
            <span v-if="result.PageAge" class="web-search-result-age">{{ result.PageAge }}</span>
          </div>
          <details v-if="snippet(result)" class="web-search-result-snippet">
            <summary>View snippet</summary>
            <div class="web-search-result-snippet-body">
              <InlineText :text="snippet(result)!" />
            </div>
          </details>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";
import InlineText from "../InlineText.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const query = computed(() => {
  const ti = props.toolInput;
  if (ti && typeof ti === "object") {
    const t = ti as { query?: unknown; queries?: unknown };
    // Native server-side search can arrive as {queries: [...]} (OpenAI) or
    // {query: "..."} (Anthropic); mirror WebSearchTool.vue.
    if (typeof t.query === "string") return t.query;
    if (Array.isArray(t.queries)) {
      const qs = t.queries.filter((q): q is string => typeof q === "string");
      if (qs.length > 0) return qs.join(" / ");
    }
  }
  return "";
});

// Only URL-bearing blocks are results: a result without a clickable link is
// degenerate (e.g. sanitized-away URLs), so it is not shown as a row.
const results = computed<LLMContent[]>(() =>
  (props.toolResult || []).filter((result) => result.URL),
);

const resultCount = computed(() => results.value.length);
const showCount = computed(() => resultCount.value > 0 && !props.hasError);
const isComplete = computed(() => !props.isRunning);

const errorText = computed(() =>
  (props.toolResult || [])
    .map((result) => result.Text)
    .filter((text) => text)
    .join("\n"),
);

const emptyMessage = computed(() => {
  const first = (props.toolResult || []).find((result) => result.Text);
  return (first && first.Text) || "No results.";
});

// The backend formats each result as "Title:/URL:/Published:/Author:/Highlights:";
// the snippet is everything after the Highlights marker, plain text with
// linkified URLs. Falls back to the full text when the marker is absent.
function snippet(result: LLMContent): string {
  const text = result.Text || "";
  const idx = text.indexOf("\nHighlights:\n");
  if (idx < 0) return "";
  return text.slice(idx + "\nHighlights:\n".length).trim();
}
</script>
