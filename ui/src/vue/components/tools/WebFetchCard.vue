<!-- Tool card for the client-side WebFetch tool (page reads backed by a
     hosted read endpoint). Per-page sections with a clickable title link and
     the page body rendered as markdown. Pages the server could not fetch are
     shown as plain linkified error lines. Vendor-neutral. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📄</span>
        <span class="tool-command"
          >Web Fetch{{ headerSuffix ? ": " : "" }}<span class="web-search-query">{{
            headerSuffix
          }}</span></span
        >
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <span v-else-if="isComplete && pageCount > 0" class="tool-success">
          {{ pageCount }} page{{ pageCount !== 1 ? "s" : "" }}
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
        <div class="tool-running-text">fetching...</div>
      </div>

      <div v-else-if="hasError" class="tool-section">
        <div class="tool-label">Error:</div>
        <div class="tool-code error">{{ errorText }}</div>
      </div>

      <div v-else-if="pages.length === 0 && failures.length === 0" class="tool-section">
        <div class="tool-label">{{ emptyMessage }}</div>
      </div>

      <div v-else class="web-fetch-pages">
        <div v-for="(page, index) in pages" :key="index" class="web-fetch-page">
          <a
            :href="page.URL || ''"
            target="_blank"
            rel="noopener noreferrer"
            class="web-search-result-title"
          >
            {{ page.Title || page.URL }}
          </a>
          <div class="web-search-result-meta">
            <span class="web-search-result-url">{{ page.URL }}</span>
          </div>
          <div v-if="page.Markdown" class="web-fetch-page-body">
            <MarkdownContent :text="page.Markdown" />
          </div>
        </div>
        <div v-for="(failure, index) in failures" :key="'f' + index" class="web-fetch-error">
          <InlineText :text="failure.Text || 'Failed to fetch'" />
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
import MarkdownContent from "../MarkdownContent.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const isFailed = (result: LLMContent) => (result.Text || "").startsWith("Error fetching ");

// Pages the server failed to load (no "# title" header) are shown as error
// lines rather than as result rows.
const failures = computed<LLMContent[]>(() =>
  (props.toolResult || []).filter((result) => result.URL && isFailed(result)),
);

interface RenderedPage {
  Title?: string;
  URL?: string;
  Markdown: string;
}

const pages = computed<RenderedPage[]>(() =>
  (props.toolResult || [])
    .filter((result) => result.URL && !isFailed(result))
    .map((result) => ({
      Title: result.Title,
      URL: result.URL,
      // The Go side keeps each page's "# <title>\nURL: <url>" header in Text
      // (so the model keeps source attribution); strip that deterministic
      // prefix before rendering the body as markdown — the card already shows
      // the title and URL.
      Markdown: pageMarkdown(result),
    })),
);

const pageCount = computed(() => pages.value.length);
const isComplete = computed(() => !props.isRunning);

const headerSuffix = computed(() => {
  const urls =
    props.toolInput && typeof props.toolInput === "object"
      ? (props.toolInput as { urls?: unknown }).urls
      : undefined;
  if (Array.isArray(urls) && urls.length > 0 && typeof urls[0] === "string") {
    if (urls.length === 1) return urls[0];
    return `${urls.length} URLs`;
  }
  if (pages.value.length === 1) return pages.value[0].URL || "";
  return pageCount.value > 0 ? `${pageCount.value} pages` : "";
});

const errorText = computed(() =>
  (props.toolResult || [])
    .map((result) => result.Text)
    .filter((text) => text)
    .join("\n"),
);

const emptyMessage = computed(() => {
  const first = (props.toolResult || []).find((result) => result.Text);
  return (first && first.Text) || "No content found.";
});

function pageMarkdown(result: LLMContent): string {
  const text = result.Text || "";
  const lines = text.split("\n");
  let idx = lines.findIndex((line) => line.startsWith("URL: "));
  if (idx < 0) return text.trim();
  // Skip any optional meta lines after the URL header (Published:, Author:)
  // and leading blanks before the content body.
  idx += 1;
  while (idx < lines.length) {
    const ln = lines[idx].trim();
    if (ln === "" || ln.startsWith("Published: ") || ln.startsWith("Author: ")) {
      idx++;
      continue;
    }
    break;
  }
  return lines.slice(idx).join("\n").trim();
}
</script>
