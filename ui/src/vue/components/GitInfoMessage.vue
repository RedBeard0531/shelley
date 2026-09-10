<!-- Sub-component of Message.vue (ported from GitInfoMessage in
     components/Message.tsx). Compact git-state notification. Preserves the
     .message.message-gitinfo container, data-testid "message-gitinfo", and the
     msg-* classes for the worktree/branch/hash/copy/subject/diff link. -->
<template>
  <div
    v-if="commitHash"
    ref="containerRef"
    class="message message-gitinfo msg-gitinfo-container"
    data-testid="message-gitinfo"
    @mouseenter="refreshTour"
  >
    <span>
      <span v-if="worktree" class="msg-worktree">{{ worktree }}</span>
      <span v-if="branch" class="msg-branch">{{ branch }}</span>
      {{ branch ? " now at " : "now at " }}
      <code
        class="msg-commit-hash"
        v-tooltip.top="'Click to copy commit hash'"
        @click="handleCopyHash"
        >{{ commitHash }}</code
      >
      <button
        :class="copied ? 'msg-copy-button copied' : 'msg-copy-button'"
        v-tooltip.top="'Copy commit hash'"
        aria-label="Copy commit hash"
        @click="handleCopyHash"
      >
        <svg
          v-if="copied"
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          class="msg-icon-middle"
        >
          <polyline points="20 6 9 17 4 12" />
        </svg>
        <svg
          v-else
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          class="msg-icon-middle"
        >
          <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
        </svg>
      </button>
      <span v-if="truncatedSubject" class="msg-subject" :title="subject || undefined"
        >"{{ truncatedSubject }}"</span
      >
      <template v-if="canShowDiff">
        {{ " " }}
        <a :href="diffHref" class="msg-diff-link" @click="onDiffLinkClick">diff</a>
      </template>
      <template v-if="tourState === 'present' && canShowDiff">
        {{ " " }}
        <a
          :href="diffHref"
          class="msg-diff-link"
          data-testid="gitinfo-tour-link"
          v-tooltip.top="'Open guided commit tour'"
          @click="onDiffLinkClick"
          >tour</a
        >
      </template>
    </span>
  </div>
</template>

<script lang="ts">
interface TourProbeCacheEntry {
  promise: Promise<boolean>;
  expiresAt: number;
}

const TOUR_PROBE_TTL_MS = 30_000;
const TOUR_HOVER_RETRY_MS = 5_000;
const tourProbeCache = new Map<string, TourProbeCacheEntry>();
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api } from "../../services/api";
import type { Message as MessageType } from "../../types";

const props = defineProps<{
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
}>();

const copied = ref(false);
const containerRef = ref<HTMLElement | null>(null);
type TourState = "unknown" | "present" | "absent";
const tourState = ref<TourState>("unknown");
let tourProbeSequence = 0;
let lastForcedTourProbeAt = 0;

const parsed = computed(() => {
  let commitHash: string | null = null;
  let subject: string | null = null;
  let branch: string | null = null;
  let worktree: string | null = null;
  if (props.message.user_data) {
    try {
      const userData =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      if (userData.commit) commitHash = userData.commit;
      if (userData.subject) subject = userData.subject;
      if (userData.branch) branch = userData.branch;
      if (userData.worktree) worktree = userData.worktree;
    } catch (err) {
      console.error("Failed to parse gitinfo user_data:", err);
    }
  }
  return { commitHash, subject, branch, worktree };
});

const commitHash = computed(() => parsed.value.commitHash);
const subject = computed(() => parsed.value.subject);
const branch = computed(() => parsed.value.branch);
const worktree = computed(() => parsed.value.worktree);

const canShowDiff = computed(() => !!commitHash.value && !!props.onOpenDiffViewer);

const truncatedSubject = computed(() => {
  const s = subject.value;
  return s && s.length > 40 ? s.slice(0, 37) + "..." : s;
});

const diffHref = computed(() => {
  const params = new URLSearchParams();
  params.set("diff", commitHash.value!);
  if (worktree.value) params.set("cwd", worktree.value);
  return `${window.location.pathname}?${params.toString()}`;
});

function handleDiffClick() {
  if (commitHash.value && props.onOpenDiffViewer) {
    props.onOpenDiffViewer(commitHash.value, worktree.value || undefined);
  }
}

function onDiffLinkClick(e: MouseEvent) {
  // Respect modifier/middle-click so users can open in a new tab.
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
    return;
  }
  e.preventDefault();
  handleDiffClick();
}

async function probeTour(force = false) {
  const hash = commitHash.value;
  const cwd = worktree.value;
  if (!hash || !cwd) return;

  const key = `${cwd}\u0000${hash}`;
  const now = Date.now();
  let entry = force ? undefined : tourProbeCache.get(key);
  if (entry && entry.expiresAt <= now) {
    tourProbeCache.delete(key);
    entry = undefined;
  }
  if (!entry) {
    entry = {
      promise: api.hasGitTour(cwd, hash),
      expiresAt: now + TOUR_PROBE_TTL_MS,
    };
    tourProbeCache.set(key, entry);
  }

  const sequence = ++tourProbeSequence;
  try {
    const present = await entry.promise;
    if (sequence === tourProbeSequence) {
      tourState.value = present ? "present" : "absent";
    }
  } catch (error) {
    if (tourProbeCache.get(key) === entry) tourProbeCache.delete(key);
    console.error("Failed to check commit tour:", error);
  }
}

function refreshTour() {
  const now = Date.now();
  const force = tourState.value === "absent" && now - lastForcedTourProbeAt >= TOUR_HOVER_RETRY_MS;
  if (force) lastForcedTourProbeAt = now;
  void probeTour(force);
}

let tourObserver: IntersectionObserver | null = null;
onMounted(() => {
  if (!containerRef.value || !("IntersectionObserver" in window)) {
    void probeTour();
    return;
  }
  tourObserver = new IntersectionObserver((entries) => {
    if (!entries.some((entry) => entry.isIntersecting)) return;
    tourObserver?.disconnect();
    tourObserver = null;
    void probeTour();
  });
  tourObserver.observe(containerRef.value);
});
onBeforeUnmount(() => tourObserver?.disconnect());

function handleCopyHash(e: MouseEvent) {
  e.preventDefault();
  if (commitHash.value) {
    navigator.clipboard.writeText(commitHash.value).then(() => {
      copied.value = true;
      setTimeout(() => (copied.value = false), 1500);
    });
  }
}
</script>
