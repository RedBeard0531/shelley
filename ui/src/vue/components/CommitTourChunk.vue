<template>
  <article class="commit-tour-chunk" :data-tour-file="fileLabel">
    <div class="commit-tour-chunk-header">
      <button
        v-if="entry.trivial"
        type="button"
        class="commit-tour-chunk-toggle"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <ToolChevron :expanded="expanded" />
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </button>
      <template v-else>
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </template>
      <button
        v-tooltip.top="'Comment on this chunk'"
        type="button"
        class="commit-tour-comment-btn"
        :aria-label="`Comment on ${fileLabel} chunk`"
        @click="openChunkComment"
      >
        💬
      </button>
    </div>

    <div v-if="expanded" class="commit-tour-chunk-body">
      <MarkdownContent v-if="entry.comment" class="commit-tour-comment" :text="entry.comment" />
      <div v-if="isBinary" class="commit-tour-placeholder">
        <span>Binary file</span>
        <pre>{{ entry.patch }}</pre>
      </div>
      <div v-else-if="!isHunk" class="commit-tour-placeholder">
        <pre>{{ entry.patch || "No textual diff." }}</pre>
      </div>
      <template v-else>
        <div
          ref="diffHostEl"
          class="commit-tour-diff-host"
          :style="rendered ? undefined : { minHeight: placeholderHeight }"
          @pointerdown.capture="rememberPointerDown"
        ></div>
        <pre v-if="diffError" class="commit-tour-raw-diff">{{ entry.patch }}</pre>
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { FileDiffMetadata, FileDiffOptions, ThemeTypes, ThemesType } from "@pierre/diffs";
import { getSingularPatch } from "@pierre/diffs";
import type { GitTourPatchEntry } from "../../services/api";
import { useFileDiffInstance } from "../composables/fileDiffInstance";
import {
  patchLineText,
  type TourCommentTarget,
  type TourDiffSide,
} from "../composables/tourComments";
import { useNearViewport } from "../composables/nearViewport";
import MarkdownContent from "./MarkdownContent.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{
  entry: GitTourPatchEntry;
  themeType: ThemeTypes;
  sideBySide: boolean;
  overflow: "scroll" | "wrap";
}>();
const emit = defineEmits<{
  (e: "comment", target: TourCommentTarget): void;
  (e: "line-comment", target: TourCommentTarget): void;
}>();

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };
const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
const MAX_CLICK_MOVEMENT_SQUARED = 25;
const expanded = ref(!props.entry.trivial);
const diffHostEl = ref<HTMLElement | null>(null);
const nearViewport = useNearViewport(diffHostEl);
let pointerDownPos: { x: number; y: number } | null = null;

watch(
  () => [props.entry.patch, props.entry.trivial] as const,
  () => {
    expanded.value = !props.entry.trivial;
  },
);

interface MarkerPath {
  present: boolean;
  path: string | null;
}

// unquoteGitPath decodes git's C-style quoted paths ("caf\303\251" etc.).
function unquoteGitPath(path: string): string {
  if (!path.startsWith('"') || !path.endsWith('"')) return path;
  const inner = path.slice(1, -1);
  const encoder = new TextEncoder();
  const bytes: number[] = [];
  let i = 0;
  while (i < inner.length) {
    const backslash = inner.indexOf("\\", i);
    if (backslash === -1) {
      bytes.push(...encoder.encode(inner.slice(i)));
      break;
    }
    // Encode the literal span whole so surrogate pairs stay intact
    // (raw non-ASCII appears when core.quotePath=false).
    if (backslash > i) bytes.push(...encoder.encode(inner.slice(i, backslash)));
    const next = inner[backslash + 1];
    if (next >= "0" && next <= "7") {
      bytes.push(parseInt(inner.slice(backslash + 1, backslash + 4), 8));
      i = backslash + 4;
    } else {
      const escapes: Record<string, number> = {
        a: 7,
        b: 8,
        t: 9,
        n: 10,
        v: 11,
        f: 12,
        r: 13,
        '"': 34,
        "\\": 92,
      };
      const code = escapes[next];
      if (code === undefined) bytes.push(...encoder.encode(next));
      else bytes.push(code);
      i = backslash + 2;
    }
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

function markerPath(patch: string, marker: "--- " | "+++ "): MarkerPath {
  let line: string | undefined;
  for (const candidate of patch.split("\n")) {
    if (HUNK_HEADER.test(candidate)) break;
    if (candidate.startsWith(marker)) line = candidate;
  }
  if (!line) return { present: false, path: null };

  let path = unquoteGitPath(line.slice(marker.length).trim());
  if (path === "/dev/null") return { present: true, path: null };
  if (path.startsWith("a/") || path.startsWith("b/")) path = path.slice(2);
  return { present: true, path };
}

// hunklessPath extracts a path for fragments without ---/+++ markers
// (binary, rename-only, mode-only) from "rename to" or "diff --git" lines.
function hunklessPath(patch: string): string | null {
  for (const line of patch.split("\n")) {
    if (line.startsWith("rename to ")) return unquoteGitPath(line.slice("rename to ".length));
  }
  // Quoted form: diff --git "a/X" "b/X" (quotes cover the prefix too).
  const quoted = /^diff --git "a\/(.*)" "b\/(.*)"$/m.exec(patch);
  if (quoted) return unquoteGitPath(`"${quoted[2]}"`);
  // Without a rename the two sides are the same path, so the text after
  // "diff --git a/" has the shape "X b/X" — recover X even if it contains " b/".
  const git = /^diff --git a\/(.*) b\/(.*)$/m.exec(patch);
  if (!git) return null;
  if (git[1] === git[2]) return git[1];
  const s = `${git[1]} b/${git[2]}`;
  const x = s.slice(0, (s.length - 3) / 2);
  return s === `${x} b/${x}` ? x : git[2];
}

const paths = computed(() => {
  const oldMarker = markerPath(props.entry.patch, "--- ");
  const newMarker = markerPath(props.entry.patch, "+++ ");
  return {
    old: oldMarker.path,
    new: newMarker.path,
    newFile: oldMarker.present && oldMarker.path === null,
    deletedFile: newMarker.present && newMarker.path === null,
    label: newMarker.path || oldMarker.path || hunklessPath(props.entry.patch) || "File change",
  };
});
const fileLabel = computed(() => paths.value.label);

interface HunkRange {
  line: string;
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
}

const hunkRanges = computed<HunkRange[]>(() => {
  const ranges: HunkRange[] = [];
  for (const line of props.entry.patch.split("\n")) {
    const match = HUNK_HEADER.exec(line);
    if (!match) continue;
    ranges.push({
      line,
      oldStart: Number(match[1]),
      oldCount: match[2] === undefined ? 1 : Number(match[2]),
      newStart: Number(match[3]),
      newCount: match[4] === undefined ? 1 : Number(match[4]),
    });
  }
  return ranges;
});

const displayRange = computed<[number, number] | null>(() => {
  const ranges = hunkRanges.value;
  if (ranges.length === 0) return null;
  const useOldSide = ranges.every((range) => range.newCount === 0);
  const first = ranges[0];
  const last = ranges[ranges.length - 1];
  const start = useOldSide ? first.oldStart : first.newStart;
  const lastStart = useOldSide ? last.oldStart : last.newStart;
  const lastCount = useOldSide ? last.oldCount : last.newCount;
  return [start, lastStart + Math.max(lastCount, 1) - 1];
});

const chunkLabel = computed(() => {
  const range = displayRange.value;
  return range ? `${fileLabel.value} · lines ${range[0]}–${range[1]}` : fileLabel.value;
});
const chunkStats = computed(() => {
  let additions = 0;
  let deletions = 0;
  let inHunk = false;
  for (const line of props.entry.patch.split("\n")) {
    if (HUNK_HEADER.test(line)) {
      inHunk = true;
      continue;
    }
    if (!inHunk) continue;
    if (line.startsWith("+")) additions++;
    else if (line.startsWith("-")) deletions++;
  }
  return { additions, deletions };
});

const isHunk = computed(() => hunkRanges.value.length > 0);
const isBinary = computed(() =>
  /^(?:GIT binary patch|Binary files .* differ)$/m.test(props.entry.patch),
);

const fileDiff = computed<FileDiffMetadata | null>(() => {
  if (!expanded.value || !nearViewport.value || !isHunk.value) return null;
  try {
    return getSingularPatch(props.entry.patch);
  } catch (error) {
    console.warn("Commit tour diff parse error:", error);
    return null;
  }
});

function rememberPointerDown(event: PointerEvent) {
  if (!event.isPrimary || event.button !== 0) {
    pointerDownPos = null;
    return;
  }
  pointerDownPos = { x: event.clientX, y: event.clientY };
}

function isPlainLineClick(event: PointerEvent): boolean {
  const start = pointerDownPos;
  pointerDownPos = null;
  if (start) {
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (dx * dx + dy * dy > MAX_CLICK_MOVEMENT_SQUARED) return false;
  }
  return window.getSelection()?.isCollapsed !== false;
}

function openChunkComment() {
  const hunk = hunkRanges.value[0]?.line || "file change";
  emit("comment", {
    where: `${fileLabel.value} chunk`,
    reference: `${fileLabel.value} (${hunk})`,
  });
}

function openLineComment(side: TourDiffSide, lineNumber: number) {
  const selectedText = patchLineText(props.entry.patch, side, lineNumber);
  if (selectedText === null) return;
  const path = (side === "deletions" ? paths.value.old : paths.value.new) || fileLabel.value;
  const sideLabel = side === "deletions" ? "old" : "new";
  emit("line-comment", {
    where: `${path} line ${lineNumber} (${sideLabel})`,
    reference: `${path}:${lineNumber}`,
    selectedText,
    quoteCode: true,
  });
}

const diffOptions = computed<FileDiffOptions<undefined>>(() => ({
  diffStyle:
    props.sideBySide && !paths.value.newFile && !paths.value.deletedFile ? "split" : "unified",
  theme: DIFF_THEMES,
  themeType: props.themeType,
  disableFileHeader: true,
  overflow: props.overflow,
  lineHoverHighlight: "line",
  onLineClick: ({ annotationSide, lineNumber, event }) => {
    if (isPlainLineClick(event)) openLineComment(annotationSide, lineNumber);
  },
  onLineNumberClick: ({ annotationSide, lineNumber }) => {
    pointerDownPos = null;
    openLineComment(annotationSide, lineNumber);
  },
}));

const placeholderHeight = computed(
  () => `${Math.min(Math.max(props.entry.patch.split("\n").length, 4) * 20, 1600)}px`,
);
const diffError = computed(
  () => expanded.value && nearViewport.value && isHunk.value && fileDiff.value == null,
);

const { rendered } = useFileDiffInstance(diffHostEl, () => {
  if (!fileDiff.value) return null;
  return { fileDiff: fileDiff.value, options: diffOptions.value };
});
</script>

<style scoped>
.commit-tour-chunk {
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 0.5rem;
  background: var(--bg-base);
}

.commit-tour-chunk-header {
  min-height: 2.5rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.commit-tour-chunk-toggle {
  min-width: 0;
  flex: 1;
  align-self: stretch;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: -0.5rem 0 -0.5rem -0.75rem;
  padding: 0.5rem 0.75rem;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.commit-tour-chunk-toggle:hover {
  background: var(--bg-tertiary);
}

.commit-tour-comment-btn {
  flex: 0 0 auto;
  width: 1.75rem;
  height: 1.75rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  opacity: 0;
  cursor: pointer;
}

.commit-tour-chunk-header:hover .commit-tour-comment-btn,
.commit-tour-comment-btn:focus-visible {
  opacity: 1;
}

.commit-tour-comment-btn:hover {
  background: var(--bg-tertiary);
}

.commit-tour-chunk-header code {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
  font-size: 0.8125rem;
}

.commit-tour-chunk-stats {
  display: inline-flex;
  gap: 0.375rem;
  margin-left: auto;
  flex: 0 0 auto;
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  font-weight: 600;
}

.commit-tour-additions {
  color: var(--green-text, #16a34a);
}

.commit-tour-deletions {
  color: var(--red-text, #dc2626);
}

.commit-tour-chunk-body {
  min-width: 0;
}

.commit-tour-comment {
  padding: 0.875rem 1rem;
  border-top: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-secondary);
}

.commit-tour-comment :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-comment :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-diff-host,
.commit-tour-diff-host :deep(diffs-container) {
  display: block;
  min-width: 0;
}

.commit-tour-diff-host :deep(diffs-container) {
  contain: content;
}

.commit-tour-placeholder,
.commit-tour-raw-diff {
  margin: 0;
  padding: 0.875rem 1rem;
  color: var(--text-secondary);
  background: var(--bg-base);
  font-size: 0.75rem;
}

.commit-tour-placeholder > span {
  display: block;
  margin-bottom: 0.5rem;
  font-weight: 600;
}

.commit-tour-placeholder pre,
.commit-tour-raw-diff {
  margin: 0;
  overflow-x: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono, monospace);
}

@media (max-width: 767px) {
  .commit-tour-chunk {
    border-right: 0;
    border-left: 0;
    border-radius: 0;
  }

  .commit-tour-chunk-header {
    padding-right: 0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment-btn {
    opacity: 1;
  }

  .commit-tour-chunk-toggle {
    margin-left: -0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment,
  .commit-tour-placeholder,
  .commit-tour-raw-diff {
    padding: 0.75rem;
  }
}
</style>
