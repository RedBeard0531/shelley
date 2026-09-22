<!-- Vue port of components/MarkdownContent.tsx. Renders sanitized markdown HTML
     via v-html. The pure pipeline lives in utils/markdownRender.ts.
     Preserves the .markdown-content .break-words container contract.

     With `commentable`, images in the rendered markdown open the image
     annotation view when clicked (or activated from the keyboard). That is
     opt-in because the view is hosted by ChatInterface: the export page renders
     the same markdown with no host, and an image that announces itself as a
     button and then does nothing is worse than a plain image. -->
<template>
  <div
    ref="containerRef"
    class="markdown-content break-words"
    :class="{ 'markdown-refs': renderFileRefs }"
    @click="onActivate"
    @keydown="onActivate"
    @auxclick="onAuxActivate"
    v-html="html"
  ></div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onBeforeUpdate, ref, watch } from "vue";
import { highlightCode, normalizeCodeLanguage } from "../../services/markdownHighlight";
import {
  addCodeBlockHeaders,
  codeBlockText,
  setCodeBlockCopied,
} from "../../utils/codeBlockCopy";
import { applyHighlightTokens } from "../../utils/codeHighlight";
import { COMMENT_ICON } from "../../utils/icons";
import { localhostLinkOptionsFromInit } from "../../utils/linkify";
import {
  parseCommitRef,
  parseFileRef,
  renderMarkdownToSafeHTML,
  type MarkdownRenderInfo,
} from "../../utils/markdownRender";
import { perfWrap } from "../../utils/perf";
import { handleImageCommentClick, openImageComment } from "../composables/imageComment";
import { OpenFileEditorKey } from "../composables/fileEditor";
import { OpenCommitViewerKey } from "../composables/commitViewer";
import { whenNearViewport } from "../composables/nearViewport";

const props = defineProps<{
  text: string;
  // When set, local-path markdown images (relative or absolute file paths) are
  // rewritten to the per-message file endpoint and rendered. Without it we
  // cannot authorize a local file, so such images are dropped.
  messageId?: string;
  // Make images here open the annotation view. Only for hosts rendered inside
  // ChatInterface, which owns that view. Read as a fixed property of the host,
  // not something toggled on a mounted instance: turning it off would need the
  // wrappers below torn down again.
  commentable?: boolean;
  // Object whose lifetime bounds the render cache entry (the owning,
  // immutable Message). Omitted by callers whose text isn't tied to a
  // stable, immutable object — the streaming preview, the distillation
  // preview, export — which always re-render.
  cacheOwner?: object;
  // Distinguishes multiple markdown runs within the same cacheOwner (e.g. a
  // message with several text blocks split by tool calls). Required
  // whenever cacheOwner is set.
  runKey?: string;
  // Rewrite VM-local links for user-clickable assistant content only.
  rewriteLocalhostLinks?: boolean;
  // Working directory the message containing this markdown was emitted under
  // (stamped by the server at record time). File references resolve against
  // it rather than the conversation's current cwd; without it the opener
  // falls back to the current cwd.
  cwd?: string;
  // Render file references (`📄path:line`) and commit references (`⎇hash`) as
  // chips that open the editor / commit viewer. Off
  // by default: references are a feature of agent replies, so only the hosts
  // rendering those ask for it (a host with no opener to call passes nothing).
  fileRefs?: boolean;
  // Live-streaming text (the chat streaming preview): the trailing fenced
  // block's fence may still be open, so its highlighting must wait for the
  // fence to close rather than tokenize half-written code on every token.
  // Omitted for static renders — a genuinely unterminated fence in final
  // text still highlights normally.
  live?: boolean;
}>();

const containerRef = ref<HTMLDivElement | null>(null);

// Hosts for file-reference and commit-reference clicks (message views are under
// ChatInterface, which provides the commit-viewer opener, and under App, which
// provides the editor opener). A reference is only rendered when a host both
// says its text is the agent's and has somewhere to open it.
const fileOpener = inject(OpenFileEditorKey, null);
const commitOpener = inject(OpenCommitViewerKey, null);
const renderFileRefs = computed(
  () => props.fileRefs === true && fileOpener !== null && commitOpener !== null,
);

// Highlighting swaps a block's single text node for one span per token —
// measured at 22% of all DOM elements in a large conversation when done
// eagerly, nearly all of it far off-screen. Defer each block until it comes
// within a viewport of view (same shared observer that gates tool cards),
// then tokenize.
let cancelDeferred: (() => void)[] = [];
const copyFeedbackTimers = new Map<HTMLButtonElement, ReturnType<typeof setTimeout>>();

// Streaming markdown re-renders (and v-html replaces the whole subtree) on
// every token. Completed (closed-fence) blocks keep their highlighted DOM
// across those replacements: right before each update, capture every
// fully-highlighted block's (language, source -> innerHTML); after the new
// pass, restore exact matches directly instead of re-tokenizing — so a block
// colors in once, when its fence closes, and is not re-tokenized or flickered
// on later tokens. The still-open trailing block is left plain until its
// fence closes (see highlightFencedCode).
const codeSnapshot = new Map<string, string>();
onBeforeUpdate(() => {
  codeSnapshot.clear();
  const root = containerRef.value;
  if (!root) return;
  for (const code of root.querySelectorAll<HTMLElement>("pre > code")) {
    const language = code.dataset.shelleyCodeHighlight;
    if (!language || language === "deferred" || language === "pending") continue;
    codeSnapshot.set(`${language}\0${code.textContent}`, code.innerHTML);
  }
});

onBeforeUnmount(() => {
  for (const cancel of cancelDeferred) cancel();
  cancelDeferred = [];
  clearCopyFeedback();
});

// Parse-state of the current render, read by the post-flush watch below.
const renderInfo: MarkdownRenderInfo = { endsInOpenFence: false };

const html = computed(
  perfWrap("markdown.render", () =>
    renderMarkdownToSafeHTML(
      props.text,
      props.messageId,
      props.cacheOwner && props.runKey !== undefined
        ? {
            owner: props.cacheOwner,
            // Distinguish renders of one run that differ: localhost link
            // rewriting, and whether references are rendered as chips.
            runKey: `${props.runKey}:${props.rewriteLocalhostLinks ? "links" : ""}:${renderFileRefs.value ? "refs" : ""}`,
          }
        : undefined,
      props.rewriteLocalhostLinks ? localhostLinkOptionsFromInit() : undefined,
      renderInfo,
      renderFileRefs.value,
    ),
  ),
);

// Images inside a link are excluded throughout: there the image is the link's
// label, so the anchor owns activation and calling it a button would both
// mis-announce it and add a redundant tab stop. The badge wrapper is a <span>,
// not an <a>, so `closest("a")` stays an accurate test after wrapping.
function isCommentable(img: HTMLImageElement): boolean {
  return !!props.commentable && !img.parentElement?.closest("a");
}

// Give commentable images button semantics, a tab stop, and the hover badge.
// Done in bulk after each render (v-html replaces the subtree) rather than
// per-image, which is also why activation is handled by one delegated listener
// below. The wrapper is what the badge positions against, matching
// CommentableImage.vue's markup so both get the same affordance.
watch(
  [html, containerRef],
  () => {
    // Cancel before the null guard: if the container vanished, stale
    // registrations would otherwise pin the detached subtree via the
    // observer's target set.
    for (const cancel of cancelDeferred) cancel();
    cancelDeferred = [];
    clearCopyFeedback();
    const root = containerRef.value;
    if (!root) return;
    if (props.commentable) {
      for (const img of root.querySelectorAll("img")) {
        // The wrapper marks an image as already done: this runs whenever the
        // container ref settles, not only when the HTML is replaced.
        if (!isCommentable(img) || img.closest(".commentable-image-link")) continue;
        img.setAttribute("role", "button");
        img.setAttribute("tabindex", "0");
        img.classList.add("commentable-image");
        const wrap = document.createElement("span");
        wrap.className = "commentable-image-link";
        img.replaceWith(wrap);
        wrap.append(img, badge());
      }
    }
    addCodeBlockHeaders(root);
    highlightFencedCode(root);
  },
  { flush: "post", immediate: true },
);

function languageFor(code: HTMLElement): string | undefined {
  for (const className of code.classList) {
    const match = /^language-(.+)$/.exec(className);
    if (match) return normalizeCodeLanguage(match[1]);
  }
  return undefined;
}

function highlightFencedCode(root: HTMLElement): void {
  // Live streaming: when the text ends inside an unterminated fence, that
  // trailing block is still receiving tokens — tokenizing it now would be
  // thrown away next token. Leave it plain until the fence closes (and a
  // later pass tokenizes it); all preceding blocks are already complete.
  const openFenceActive = !!props.live && renderInfo.endsInOpenFence;
  const codes = root.querySelectorAll<HTMLElement>("pre > code");
  const lastCode = codes.length > 0 ? codes[codes.length - 1] : null;
  for (const code of codes) {
    const state = code.dataset.shelleyCodeHighlight;
    if (state && state !== "deferred") continue;
    const language = languageFor(code);
    if (!language) continue;

    // Unchanged block (a fence that closed in a previous pass): restore the
    // highlighted DOM captured before this v-html replacement — no
    // re-tokenize, no flicker. Snapshot misses both brand-new blocks and the
    // still-growing trailing block, which fall through below.
    const snapshotHtml = codeSnapshot.get(`${language}\0${code.textContent}`);
    if (snapshotHtml !== undefined) {
      code.innerHTML = snapshotHtml;
      code.dataset.shelleyCodeHighlight = language;
      continue;
    }

    if (openFenceActive && code === lastCode) continue;

    // "deferred" blocks re-register on every pass (the watch cancels all
    // previous registrations first) because v-html replacement may have
    // produced brand-new elements.
    code.dataset.shelleyCodeHighlight = "deferred";
    cancelDeferred.push(
      whenNearViewport(code, () => highlightBlock(root, code, language), {
        printReveal: false,
      }),
    );
  }
}

function highlightBlock(root: HTMLElement, code: HTMLElement, language: string): void {
  // The observer can fire in the window between a v-html replacement and the
  // post-flush watch pass that would have canceled this registration.
  if (!code.isConnected) return;
  const source = code.textContent ?? "";
  code.dataset.shelleyCodeHighlight = "pending";
  void highlightCode(language, source)
    .then((result) => {
      // v-html can replace this code block while the worker is still tokenizing.
      if (!root.contains(code) || code.dataset.shelleyCodeHighlight !== "pending") return;
      if (code.textContent !== source) return;
      if (result.kind === "unknown") {
        delete code.dataset.shelleyCodeHighlight;
        return;
      }
      applyHighlightTokens(code, source, result.lines);
      code.dataset.shelleyCodeHighlight = language;
    })
    .catch((error: unknown) => {
      if (root.contains(code) && code.dataset.shelleyCodeHighlight === "pending") {
        delete code.dataset.shelleyCodeHighlight;
      }
      console.error("Syntax highlighting failed", error);
    });
}

function badge(): HTMLElement {
  const el = document.createElement("span");
  el.className = "commentable-image-badge";
  el.setAttribute("aria-hidden", "true");
  el.innerHTML = `${COMMENT_ICON} Comment`;
  return el;
}

function clearCopyFeedback(): void {
  for (const [button, timer] of copyFeedbackTimers) {
    clearTimeout(timer);
    setCodeBlockCopied(button, false);
  }
  copyFeedbackTimers.clear();
}

async function copyCodeBlock(button: HTMLButtonElement): Promise<void> {
  const text = codeBlockText(button);
  if (text === undefined) return;

  try {
    await navigator.clipboard.writeText(text);
    const previousTimer = copyFeedbackTimers.get(button);
    if (previousTimer) clearTimeout(previousTimer);
    setCodeBlockCopied(button, true);
    const timer = setTimeout(() => {
      setCodeBlockCopied(button, false);
      copyFeedbackTimers.delete(button);
    }, 1500);
    copyFeedbackTimers.set(button, timer);
  } catch (error) {
    console.error("Copying code failed", error);
  }
}

// Activate a reference chip: a file reference opens the editor at the line it
// names, selecting the range when it carries one (a reference without a line
// opens at line 1); a commit reference opens the commit viewer at that commit.
// The reference is read back out of the chip's own text, so a chip cannot open
// something other than what it displays. Returns whether the event targeted a
// reference.
function onRefActivate(e: MouseEvent | KeyboardEvent): boolean {
  const anchor = (e.target as HTMLElement | null)?.closest?.("a.file-ref, a.commit-ref");
  // A chip is only live where this host renders references (text the agent
  // wrote, and an opener to activate). HTML claiming the class anywhere else —
  // a fetched page, tool output, a tour annotation — is inert rather than a way
  // into the editor.
  if (!anchor || !renderFileRefs.value) return false;
  // Claim the event before any refusal, so an inert href cannot act as a jump.
  e.preventDefault();
  // One text node: a child element can be styled away, which would let a label
  // show one path while contributing a different one to textContent.
  if (anchor.children.length > 0) return true;
  // A chip the reader cannot see must not open anything: CSS can hide it (a
  // clipping class, a wrapper) and it is still in the tab order, so Enter on an
  // invisible chip would otherwise open something nobody can read. Require the
  // label to fit inside the box the anchor paints. Not airtight — a wrapper can
  // clip the anchor and its text together — but it stops the cases that hide the
  // whole label or crop it.
  const box = anchor.getBoundingClientRect();
  const label = document.createRange();
  label.selectNodeContents(anchor);
  const painted = label.getBoundingClientRect();
  if (painted.width > box.width + 1 || painted.height > box.height + 1) return true;
  // Layout is not paint: a class can make the chip fully transparent, which the
  // rect check above cannot see.
  if (!anchor.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return true;
  // Autorepeat must not reopen the viewer for every repeat.
  if (e instanceof KeyboardEvent && e.repeat) return true;
  if (anchor.classList.contains("commit-ref")) {
    const ref = parseCommitRef(anchor.textContent ?? "");
    if (ref && commitOpener) commitOpener(ref.hash);
  } else {
    const ref = parseFileRef(anchor.textContent ?? "");
    if (ref && fileOpener) {
      fileOpener(ref.path, { line: ref.line ?? 1, endLine: ref.endLine, baseDir: props.cwd || undefined });
    }
  }
  return true;
}

// A middle-click or ctrl-click on a reference would otherwise follow the inert
// href and open a second copy of the app in a new tab; the editor is the only
// destination a reference has.
function onAuxActivate(e: MouseEvent) {
  if ((e.target as HTMLElement | null)?.closest?.("a.file-ref, a.commit-ref")) e.preventDefault();
}

function onActivate(e: MouseEvent | KeyboardEvent) {
  const target = e.target;
  if (e instanceof MouseEvent && target instanceof Element) {
    const button = target.closest<HTMLButtonElement>(".shelley-code-copy");
    if (button) {
      void copyCodeBlock(button);
      return;
    }
  }

  if (e instanceof KeyboardEvent) {
    // Keyboard activation of a focused reference: Enter natively synthesizes
    // a click (handled by the MouseEvent path below), but Space does not
    // activate links, and either key would otherwise also trigger the
    // href="#" default jump. Handle both explicitly; preventDefault stops
    // the default so no synthetic click follows.
    if (!e.repeat && (e.key === "Enter" || e.key === " ") && onRefActivate(e)) return;
  } else if (onRefActivate(e)) {
    return;
  }
  const img = e.target;
  if (!(img instanceof HTMLImageElement) || !isCommentable(img)) return;
  if (e instanceof KeyboardEvent) {
    // Only the activation keys, and only once the target is known to be an
    // image: a blanket space-prevent here would cost every message its
    // space-to-scroll. Autorepeat is ignored so holding a key doesn't churn.
    if (e.repeat || (e.key !== "Enter" && e.key !== " ")) return;
    e.preventDefault();
    openImageComment({ src: img.src });
    return;
  }
  handleImageCommentClick(e, { src: img.src });
}
</script>
