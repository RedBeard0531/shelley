<template>
  <div ref="viewRef" class="commit-tour-view" data-review="Tour" data-review-scroll @scroll.passive="handleScroll">
    <article ref="documentRef" class="commit-tour-document">
      <div
        v-if="commitMessage || tour.tour.title || tour.tour.intro"
        :id="TOUR_OVERVIEW_ANCHOR"
        :data-tour-anchor="TOUR_OVERVIEW_ANCHOR"
        class="commit-tour-overview"
      >
        <section v-if="commitMessage" class="commit-tour-commit-message" data-review="Commit message" data-review-item>
          <div class="commit-tour-commit-meta">
            <code :title="commitMessage.hash">{{ commitMessage.hash.slice(0, 8) }}</code>
            <span>{{ commitMessage.author }}</span>
          </div>
          <h2>{{ commitMessage.subject }}</h2>
          <details v-if="commitMessage.body.trim()" open class="commit-tour-commit-body">
            <summary>
              <span class="commit-tour-commit-chevron" aria-hidden="true">›</span>
              Full message
            </summary>
            <pre>{{ commitMessage.body }}</pre>
          </details>
        </section>

        <header v-if="tour.tour.title || tour.tour.intro" class="commit-tour-introduction" data-review="Intro" data-review-item>
          <h1 v-if="tour.tour.title">{{ tour.tour.title }}</h1>
          <MarkdownContent v-if="tour.tour.intro" :text="tour.tour.intro" />
        </header>
      </div>

      <CommitTourItems
        v-if="tour.tour.decisions?.length"
        :id="TOUR_DECISIONS_ANCHOR"
        :data-tour-anchor="TOUR_DECISIONS_ANCHOR"
        kind="decision"
        :items="tour.tour.decisions"
        @comment="(item, index) => openItemComment('decision', item, index)"
      />
      <CommitTourItems
        v-if="tour.tour.questions?.length"
        :id="TOUR_QUESTIONS_ANCHOR"
        :data-tour-anchor="TOUR_QUESTIONS_ANCHOR"
        kind="question"
        :items="tour.tour.questions"
        @comment="(item, index) => openItemComment('question', item, index)"
      />

      <template v-for="(entry, position) in tour.tour.chunks" :key="entryKey(entry, position)">
        <MarkdownContent
          v-if="isHeaderEntry(entry)"
          :id="tourEntryAnchor(position)"
          class="commit-tour-section-heading"
          :data-tour-anchor="tourEntryAnchor(position)"
          :data-review="sections[position]"
          data-review-item
          :text="entry.header"
        />
        <CommitTourMedia
          v-else-if="isMediaEntry(entry)"
          :id="tourEntryAnchor(position)"
          :data-tour-anchor="tourEntryAnchor(position)"
          :data-review="sections[position] ? `${sections[position]} › ${entry.name}` : entry.name"
          data-review-item
          :entry="entry"
          :src="api.gitTourMediaURL(cwd ?? '', entry.blob)"
          @comment="emit('open-comment', $event)"
        />
        <CommitTourChunk
          v-else
          :id="tourEntryAnchor(position)"
          :data-tour-anchor="tourEntryAnchor(position)"
          :entry="entry"
          :section="sections[position]"
          :expanded="!entry.trivial || expandedAnchors.has(tourEntryAnchor(position))"
          :theme-type="themeType"
          :side-by-side="sideBySide"
          :overflow="overflowPreference"
          :load-file="cwd ? loadFile : undefined"
          @update:expanded="emit('expand-change', tourEntryAnchor(position), $event)"
          @comment="emit('open-comment', $event)"
          @line-comment="emit('open-comment', $event)"
          @layout-change="releaseNavigation"
        />
      </template>
    </article>

    <button
      v-if="selectionPrompt"
      v-tooltip.top="'Add comment on selection'"
      type="button"
      class="diff-viewer-comment-prompt"
      :style="{
        position: 'fixed',
        top: `${selectionPrompt.top}px`,
        left: `${selectionPrompt.left}px`,
      }"
      @mousedown.prevent
      @click="openSelectionComment"
    >
      💬 Comment
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import type { ThemeTypes } from "@pierre/diffs";
import { api } from "../../services/api";
import type { GitTourEntry, GitTourHeaderEntry, GitTourItem, GitTourResponse } from "../../services/api";
import type { GitCommitMessage, GitFileDiff } from "../../types";
import { isDarkModeActive } from "../../services/theme";
import { useOverflowPreference, useSideBySidePreference } from "../composables/diffViewPreference";
import type { TourCommentTarget } from "../composables/tourComments";
import CommitTourChunk from "./CommitTourChunk.vue";
import CommitTourItems from "./CommitTourItems.vue";
import CommitTourMedia from "./CommitTourMedia.vue";
import MarkdownContent from "./MarkdownContent.vue";
import {
  TOUR_DECISIONS_ANCHOR,
  TOUR_OVERVIEW_ANCHOR,
  TOUR_QUESTIONS_ANCHOR,
  headerLabel,
  isMediaEntry,
  tourEntryAnchor,
} from "./commitTourContents";

const props = defineProps<{
  tour: GitTourResponse;
  commitMessage: GitCommitMessage | null;
  expandedAnchors: Set<string>;
  // Repository directory the tour was loaded from; needed to fetch whole-file
  // contents for the chunks' full-file mode.
  cwd?: string;
}>();
const emit = defineEmits<{
  (e: "open-comment", target: TourCommentTarget): void;
  (e: "active-anchor-change", anchor: string): void;
  (e: "expand-change", anchor: string, expanded: boolean): void;
}>();

const themeType = ref<ThemeTypes>(isDarkModeActive() ? "dark" : "light");
const isMobile = ref(window.innerWidth < 768);
const { sideBySidePreference } = useSideBySidePreference();
const sideBySide = computed(() => !isMobile.value && sideBySidePreference.value);
const { overflowPreference } = useOverflowPreference();
const shortHash = computed(() => props.tour.hash.slice(0, 8));

// Whole-file contents at the toured commit, shared by every chunk of the same
// file so expanding a second chunk does not refetch. Keyed by both paths since
// a rename changes the left-hand side.
const fileContentsCache = new Map<string, Promise<GitFileDiff>>();
watch([() => props.tour.hash, () => props.cwd], () => fileContentsCache.clear());

function loadFile(oldPath: string | null, newPath: string | null): Promise<GitFileDiff> {
  const cwd = props.cwd ?? "";
  const path = newPath ?? oldPath ?? "";
  const key = `${oldPath ?? ""}\0${newPath ?? ""}`;
  let pending = fileContentsCache.get(key);
  if (!pending) {
    pending = api
      .getGitFileDiff(
        props.tour.hash,
        path,
        cwd,
        "self",
        oldPath && oldPath !== path ? oldPath : undefined,
      )
      .catch((error: unknown) => {
        fileContentsCache.delete(key);
        throw error;
      });
    fileContentsCache.set(key, pending);
  }
  return pending;
}
const viewRef = ref<HTMLElement | null>(null);
const documentRef = ref<HTMLElement | null>(null);
const selectionPrompt = ref<{
  top: number;
  left: number;
  target: TourCommentTarget;
} | null>(null);
let themeObserver: MutationObserver | null = null;
let tourResizeObserver: ResizeObserver | null = null;
let selectionFrame: number | null = null;
let scrollFrame: number | null = null;
let activeAnchor = "";
// Retain an explicit selection through lazy layout and bottom clamping, but
// release it on any independent scroll (including focus and native scrollbars).
let navigationTarget: HTMLElement | null = null;
// True while a scroll event is the expected result of alignNavigationTarget's
// own scrollIntoView. The diffs' virtualizer changes content height around
// every big scroll, so scroll *position* comparison cannot tell a programmatic
// re-alignment from a user scroll; an explicit flag can.
let programmaticScroll = false;

function handleScroll() {
  if (programmaticScroll) {
    // Our own re-alignment landed; the navigation target stays in charge
    // until an independent scroll releases it.
    programmaticScroll = false;
  } else {
    // An independent scroll (user wheel, focus, native scrollbars) releases
    // the explicit selection.
    navigationTarget = null;
  }
  scheduleActiveAnchor();
}

function alignNavigationTarget() {
  const view = viewRef.value;
  if (!view || !navigationTarget) return;
  programmaticScroll = true;
  navigationTarget.scrollIntoView({ block: "start" });
  // If the view was already at the target, no scroll event fires to clear
  // the flag; drop it on the next frame.
  requestAnimationFrame(() => {
    programmaticScroll = false;
  });
}

// Either eye controls or inline disclosure can change visibility. Release the
// previous jump before that layout change, rather than pulling the reader back.
watch(
  () => Array.from(props.expandedAnchors),
  () => {
    navigationTarget = null;
  },
  { flush: "sync" },
);

// A chunk swapping between its patch and the full file keeps its own changed
// line in place; without this the resize handler would drag the last
// table-of-contents jump back to the top instead.
function releaseNavigation() {
  navigationTarget = null;
}

function handleTourResize() {
  alignNavigationTarget();
  scheduleActiveAnchor();
}

function isHeaderEntry(entry: GitTourEntry): entry is GitTourHeaderEntry {
  return "header" in entry;
}

function entryKey(entry: GitTourEntry, position: number): string {
  const kind = isHeaderEntry(entry) ? "header" : isMediaEntry(entry) ? "media" : "patch";
  return `${kind}-${position}`;
}

function openItemComment(kind: "decision" | "question", item: GitTourItem, index: number) {
  emit("open-comment", {
    where: `${kind === "question" ? "Question" : "Decision"} ${index + 1}`,
    reference: `commit ${shortHash.value} ${kind} ${index + 1}`,
    selectedText: item.title,
  });
}

const sections = computed(() => {
  let current = "";
  return props.tour.tour.chunks.map((entry) => {
    if (isHeaderEntry(entry)) current = headerLabel(entry.header);
    return current;
  });
});

function composedClosest(node: Node | null, selector: string): HTMLElement | null {
  let current: Node | null = node;
  while (current) {
    if (current instanceof HTMLElement && current.matches(selector)) return current;
    if (current.parentNode) {
      current = current.parentNode;
      continue;
    }
    const root = current.getRootNode();
    current = root instanceof ShadowRoot ? root.host : null;
  }
  return null;
}

function updateSelectionPrompt() {
  selectionFrame = null;
  const selection = window.getSelection();
  const selectedText = selection?.toString().trim() ?? "";
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed || !selectedText) {
    selectionPrompt.value = null;
    return;
  }
  const selectedInView = composedClosest(selection.anchorNode, ".commit-tour-view");
  if (selectedInView !== viewRef.value) {
    selectionPrompt.value = null;
    return;
  }

  const rect = selection.getRangeAt(0).getBoundingClientRect();
  const chunkElement = composedClosest(selection.anchorNode, "[data-tour-file]");
  const file = chunkElement?.dataset.tourFile;
  const reference = file ? `${file} (${shortHash.value})` : `commit ${shortHash.value}`;
  selectionPrompt.value = {
    top: Math.max(8, Math.min(rect.bottom + 8, window.innerHeight - 40)),
    left: Math.max(8, Math.min(rect.right + 8, window.innerWidth - 120)),
    target: {
      where: file ? `${file} (${shortHash.value})` : `commit ${shortHash.value} narrative`,
      reference,
      selectedText,
    },
  };
}

function handleSelectionChange() {
  if (selectionFrame !== null) cancelAnimationFrame(selectionFrame);
  selectionFrame = requestAnimationFrame(updateSelectionPrompt);
}

function openSelectionComment() {
  if (!selectionPrompt.value) return;
  emit("open-comment", selectionPrompt.value.target);
  selectionPrompt.value = null;
}

function updateActiveAnchor() {
  scrollFrame = null;
  const view = viewRef.value;
  if (!view) return;

  const anchors = Array.from(view.querySelectorAll<HTMLElement>("[data-tour-anchor]"));
  if (anchors.length === 0) return;

  const activationTop = view.getBoundingClientRect().top + 24;
  let current = anchors[0].dataset.tourAnchor ?? "";
  for (const anchor of anchors) {
    if (anchor.getBoundingClientRect().top > activationTop) break;
    current = anchor.dataset.tourAnchor ?? current;
  }
  const canScroll = view.scrollHeight > view.clientHeight + 1;
  if (canScroll && view.scrollTop + view.clientHeight >= view.scrollHeight - 1) {
    current = anchors.at(-1)?.dataset.tourAnchor ?? current;
  }
  if (navigationTarget) {
    current = navigationTarget.dataset.tourAnchor ?? current;
  }
  if (!current || current === activeAnchor) return;
  activeAnchor = current;
  emit("active-anchor-change", current);
}

function scheduleActiveAnchor() {
  if (scrollFrame !== null) return;
  scrollFrame = requestAnimationFrame(updateActiveAnchor);
}

function scrollToAnchor(anchor: string) {
  navigationTarget = viewRef.value?.querySelector<HTMLElement>(`#${anchor}`) ?? null;
  alignNavigationTarget();
  scheduleActiveAnchor();
}

watch(
  () => props.tour,
  () => {
    activeAnchor = "";
    navigationTarget = null;
    nextTick(scheduleActiveAnchor);
  },
  { flush: "post" },
);

defineExpose({ scrollToAnchor });

function handleResize() {
  isMobile.value = window.innerWidth < 768;
}

onMounted(() => {
  themeObserver = new MutationObserver((mutations) => {
    if (mutations.some((mutation) => mutation.attributeName === "class")) {
      themeType.value = isDarkModeActive() ? "dark" : "light";
    }
  });
  themeObserver.observe(document.documentElement, { attributes: true });
  tourResizeObserver = new ResizeObserver(handleTourResize);
  if (viewRef.value) tourResizeObserver.observe(viewRef.value);
  if (documentRef.value) tourResizeObserver.observe(documentRef.value);
  document.addEventListener("selectionchange", handleSelectionChange);
  window.addEventListener("resize", handleResize);
  scheduleActiveAnchor();
});

onUnmounted(() => {
  themeObserver?.disconnect();
  tourResizeObserver?.disconnect();
  document.removeEventListener("selectionchange", handleSelectionChange);
  window.removeEventListener("resize", handleResize);
  if (selectionFrame !== null) cancelAnimationFrame(selectionFrame);
  if (scrollFrame !== null) cancelAnimationFrame(scrollFrame);
});
</script>

<style scoped>
.commit-tour-view {
  width: 100%;
  height: 100%;
  overflow: auto;
  background: var(--bg-base);
  color: var(--text-primary);
}

.commit-tour-document {
  min-width: 0;
  width: 100%;
  margin: 0 auto;
  padding: 0 clamp(1rem, 3vw, 2.5rem) 4rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.commit-tour-overview {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  scroll-margin-top: 1rem;
}

.commit-tour-commit-message {
  padding: 0.875rem 1rem;
  border: 1px solid color-mix(in srgb, var(--border-color) 75%, var(--accent-color, #3b82f6));
  border-radius: 0.5rem;
  background: color-mix(in srgb, var(--bg-secondary) 88%, var(--accent-color, #3b82f6));
}

.commit-tour-commit-meta {
  display: flex;
  align-items: baseline;
  gap: 0.625rem;
  color: var(--text-secondary);
  font-size: 0.75rem;
}

.commit-tour-commit-meta code {
  flex: 0 0 auto;
  color: var(--text-primary);
  font-family: var(--font-mono, monospace);
  font-weight: 600;
}

.commit-tour-commit-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-tour-commit-message h2 {
  margin: 0.375rem 0 0;
  overflow-wrap: anywhere;
  font-size: clamp(1rem, 2vw, 1.25rem);
  line-height: 1.35;
}

.commit-tour-commit-body {
  min-width: 0;
  margin-top: 0.625rem;
  border-top: 1px solid var(--border-color);
  padding-top: 0.5rem;
}

.commit-tour-commit-body summary {
  width: fit-content;
  display: flex;
  align-items: center;
  gap: 0.25rem;
  color: var(--text-secondary);
  font-size: 0.75rem;
  cursor: pointer;
  list-style: none;
}

.commit-tour-commit-body summary::-webkit-details-marker {
  display: none;
}

.commit-tour-commit-chevron {
  display: inline-block;
  font-size: 1rem;
  line-height: 1;
  transition: transform 0.15s ease;
}

.commit-tour-commit-body[open] .commit-tour-commit-chevron {
  transform: rotate(90deg);
}

.commit-tour-commit-body pre {
  margin: 0.625rem 0 0;
  overflow-x: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--text-primary);
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  line-height: 1.5;
}

.commit-tour-introduction,
.commit-tour-section-heading {
  scroll-margin-top: 1rem;
}

/* Chunks have sticky headers: a ToC jump must land the header flush at the
   pane's top. A margin here would leave the previous card's tail visible
   above the just-landed header. */
.commit-tour-chunk {
  scroll-margin-top: 0;
}

.commit-tour-introduction,
.commit-tour-section-heading {
  min-width: 0;
  overflow-wrap: anywhere;
}

.commit-tour-introduction {
  padding-bottom: 0.5rem;
}

.commit-tour-introduction h1 {
  margin: 0 0 0.75rem;
  font-size: clamp(1.5rem, 3vw, 2.125rem);
  line-height: 1.2;
}

.commit-tour-introduction :deep(.markdown-content > :first-child),
.commit-tour-section-heading :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-introduction :deep(.markdown-content > :last-child),
.commit-tour-section-heading :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-section-heading {
  margin-top: 0.75rem;
}

@media (max-width: 767px) {
  .commit-tour-document {
    padding: 0 0 5rem;
    gap: 0.875rem;
  }

  .commit-tour-commit-message,
  .commit-tour-introduction,
  .commit-tour-section-heading {
    margin-right: 0.875rem;
    margin-left: 0.875rem;
  }
}
</style>
