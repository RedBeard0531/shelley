<!-- A commit's tour build: a link to the subagent building it or, given a
     conversation to ask from, a button that requests one. Renders nothing
     while the status loads or once the tour exists, which emits `present`.
     `navigate` opens the worker's conversation (default: in place). -->
<template>
  <a
    v-if="status?.status === 'building' && status.worker_slug"
    class="commit-tour-action"
    :href="`/c/${status.worker_slug}`"
    @click="openWorker($event, status.worker_slug)"
  >
    <span class="spinner spinner-small" aria-hidden="true" />
    Building tour ↗
  </a>
  <span v-else-if="status?.status === 'building'" class="commit-tour-action">
    <span class="spinner spinner-small" aria-hidden="true" />
    Building tour
  </span>
  <button
    v-else-if="conversationId && (status?.status === 'absent' || status?.status === 'failed')"
    v-tooltip.top="status.error || 'Build a guided tour in a subagent'"
    type="button"
    class="commit-tour-action"
    :disabled="requesting"
    @click="request(conversationId)"
  >
    <span v-if="requesting" class="spinner spinner-small" aria-hidden="true" />
    {{ requesting ? "Building tour" : "Build tour" }}
  </button>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import type { GitTourBuildStatus } from "../../services/api";
import {
  loadCommitTourStatus,
  requestCommitTour,
  subscribeCommitTourStatus,
} from "../../services/commitTourStatus";
import { navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{
  cwd: string;
  hash: string;
  conversationId?: string | null;
  navigate?: (slug: string) => void;
}>();
const emit = defineEmits<{ (e: "present", hash: string): void }>();

const status = ref<GitTourBuildStatus | null>(null);
const requesting = ref(false);
// Bumped per request and per commit, so only the latest request settles `requesting`.
let requestSeq = 0;

// Follows the shared status (services/commitTourStatus), polling while a
// build runs and retrying failed probes.
watch(
  [() => props.cwd, () => props.hash],
  ([dir, hash], _, onCleanup) => {
    status.value = null;
    requesting.value = false;
    requestSeq++;
    let active = true;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const probeLater = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => void probe(), 2_000);
    };
    const update = (next: GitTourBuildStatus) => {
      if (!active) return;
      status.value = next;
      if (next.status === "building") probeLater();
      else if (timer) clearTimeout(timer);
      if (next.status === "present") emit("present", hash);
    };
    const probe = async () => {
      try {
        update(await loadCommitTourStatus(dir, hash, true));
      } catch (error) {
        if (!active) return;
        console.error("Failed to check commit tour:", error);
        probeLater();
      }
    };
    const unsubscribe = subscribeCommitTourStatus(dir, hash, update);
    void probe();
    onCleanup(() => {
      active = false;
      unsubscribe();
      if (timer) clearTimeout(timer);
    });
  },
  { immediate: true },
);

async function request(conversationId: string) {
  const { cwd, hash } = props;
  if (requesting.value) return;
  requesting.value = true;
  const seq = ++requestSeq;
  try {
    await requestCommitTour(conversationId, cwd, hash);
  } catch (error) {
    if (seq === requestSeq) {
      status.value = {
        status: "failed",
        hash,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  } finally {
    if (seq === requestSeq) requesting.value = false;
  }
}

function openWorker(e: MouseEvent, slug: string) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  (props.navigate ?? navigateToConversationSlug)(slug);
}
</script>
