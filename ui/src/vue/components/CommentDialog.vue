<!-- Draggable "Add Comment" dialog shared by DiffViewer and
     EditableFileModal (comment mode). Preserves the diff-viewer-comment-*
     class/behavior contract from DiffViewer: centered by default, explicit
     top/left once dragged, drag via the header handle, focus the textarea on
     open. v-model:text binds the comment text; "submit"/"cancel" are emitted
     for the host to handle. -->
<template>
  <div
    ref="dialogRef"
    class="diff-viewer-comment-dialog"
    :class="{ 'is-dragged': dialogPos }"
    :style="dialogPos ? { top: `${dialogPos.top}px`, left: `${dialogPos.left}px` } : undefined"
  >
    <h4 class="diff-viewer-comment-dialog-handle" @mousedown="startDialogDrag">
      <span>
        Add Comment (Line{{
          info.startLine !== info.endLine ? `s ${info.startLine}-${info.endLine}` : ` ${info.line}`
        }}<template v-if="showSide">, {{ info.side === "left" ? "old" : "new" }}</template
        >)
      </span>
    </h4>
    <pre v-if="info.selectedText" class="diff-viewer-selected-text">{{ info.selectedText }}</pre>
    <textarea
      ref="inputRef"
      :value="text"
      placeholder="Enter your comment..."
      class="diff-viewer-comment-input"
      @input="emit('update:text', ($event.target as HTMLTextAreaElement).value)"
    />
    <div class="diff-viewer-comment-actions">
      <button class="diff-viewer-btn diff-viewer-btn-secondary" @click="emit('cancel')">
        Cancel
      </button>
      <button
        class="diff-viewer-btn diff-viewer-btn-primary"
        :disabled="!text.trim()"
        @click="emit('submit')"
      >
        Add Comment
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import type { CommentDialogInfo } from "../composables/monacoComments";

const props = withDefaults(
  defineProps<{
    info: CommentDialogInfo;
    text: string;
    /** Show the ", old/new" side suffix (diff views only). */
    showSide?: boolean;
  }>(),
  { showSide: true },
);
const emit = defineEmits<{
  (e: "update:text", text: string): void;
  (e: "submit"): void;
  (e: "cancel"): void;
}>();

const dialogRef = ref<HTMLDivElement | null>(null);
const inputRef = ref<HTMLTextAreaElement | null>(null);
// Optional drag offset. Null => use the CSS-centered default position;
// otherwise an explicit top/left in px relative to the positioned ancestor
// (the fixed .diff-viewer-overlay in both hosts, whose padding box is what
// the dialog's absolute top/left resolve against).
const dialogPos = ref<{ top: number; left: number } | null>(null);
let dialogDrag: { startX: number; startY: number; baseTop: number; baseLeft: number } | null = null;

function startDialogDrag(e: MouseEvent) {
  // Ignore drags that begin on interactive controls inside the header.
  if ((e.target as HTMLElement).closest("button, textarea, input")) return;
  const el = dialogRef.value;
  if (!el) return;
  const rect = el.getBoundingClientRect();
  // Positioned ancestor that absolute top/left coordinates resolve against.
  const parent = (el.offsetParent as HTMLElement | null)?.getBoundingClientRect();
  const baseTop = parent ? rect.top - parent.top : rect.top;
  const baseLeft = parent ? rect.left - parent.left : rect.left;
  dialogDrag = { startX: e.clientX, startY: e.clientY, baseTop, baseLeft };
  window.addEventListener("mousemove", onDialogDrag);
  window.addEventListener("mouseup", endDialogDrag);
  e.preventDefault();
}

function onDialogDrag(e: MouseEvent) {
  if (!dialogDrag) return;
  dialogPos.value = {
    top: dialogDrag.baseTop + (e.clientY - dialogDrag.startY),
    left: dialogDrag.baseLeft + (e.clientX - dialogDrag.startX),
  };
}

function endDialogDrag() {
  dialogDrag = null;
  window.removeEventListener("mousemove", onDialogDrag);
  window.removeEventListener("mouseup", endDialogDrag);
}

onMounted(() => {
  setTimeout(() => inputRef.value?.focus(), 50);
});
// Retargeting the open dialog (clicking another line while text is pending)
// recenters and refocuses it, matching the old remount-per-open behavior.
watch(
  () => props.info,
  () => {
    dialogPos.value = null;
    setTimeout(() => inputRef.value?.focus(), 50);
  },
);
onUnmounted(endDialogDrag);
</script>
