<!-- Expanded drawer children for running jobs, above the conversation's subagents. -->
<template>
  <div
    :id="`background-jobs-${conversationId}`"
    :class="['drawer-background-jobs-list', { 'drawer-background-jobs-nested': nested }]"
    role="group"
    aria-label="Running background jobs"
    @click.stop
    @auxclick.stop
  >
    <div v-if="error" class="background-jobs-error" role="alert">{{ error }}</div>
    <div v-if="jobs === null" class="background-jobs-empty">{{ error ? "" : "Loading…" }}</div>
    <div v-else-if="jobs.length === 0" class="background-jobs-empty">No running jobs.</div>
    <div
      v-for="job in jobs ?? []"
      :key="job.job_id"
      class="drawer-background-job"
      data-testid="background-job"
    >
      <div class="background-jobs-command" :title="job.command">{{ job.command }}</div>
      <button
        type="button"
        class="background-jobs-stop"
        data-testid="background-job-kill"
        :disabled="killing.has(job.job_id)"
        :title="killing.has(job.job_id) ? 'Stopping…' : 'Stop background job'"
        :aria-label="`Stop background job: ${job.command}`"
        @click="stop(job.job_id)"
      >
        <svg class="drawer-icon-size" fill="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <rect x="6" y="6" width="12" height="12" rx="2" />
        </svg>
      </button>
      <span class="background-jobs-meta">{{ elapsedSince(job.started_at, now) }}</span>
      <pre
        v-if="job.tail"
        v-stick-to-bottom
        class="background-jobs-tail"
        data-testid="background-job-tail"
        >{{ job.tail }}</pre
      >
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, type BackgroundJob } from "../../services/api";
import { elapsedSince } from "./tools/toolElapsed";

const props = defineProps<{ conversationId: string; count: number; nested?: boolean }>();
const jobs = ref<BackgroundJob[] | null>(null);
const error = ref("");
const killing = ref(new Set<string>());
const now = ref(Date.now());
const wasAtBottom = new WeakMap<HTMLElement, boolean>();
const vStickToBottom = {
  mounted(el: HTMLElement) {
    el.scrollTop = el.scrollHeight;
  },
  beforeUpdate(el: HTMLElement) {
    wasAtBottom.set(el, el.scrollHeight - el.scrollTop - el.clientHeight < 8);
  },
  updated(el: HTMLElement) {
    if (wasAtBottom.get(el)) el.scrollTop = el.scrollHeight;
  },
};
let poll: number | undefined;
let fetching = false;
let fetchAgain = false;
let disposed = false;

async function refresh() {
  if (fetching) {
    fetchAgain = true;
    return;
  }
  window.clearTimeout(poll);
  fetching = true;
  try {
    const list = await api.getBackgroundJobs(props.conversationId);
    if (disposed) return;
    jobs.value = list;
    error.value = "";
    now.value = Date.now();
    killing.value = new Set([...killing.value].filter((id) => list.some((j) => j.job_id === id)));
  } catch (e) {
    if (!disposed) error.value = e instanceof Error ? e.message : String(e);
  } finally {
    fetching = false;
    if (disposed) return;
    if (fetchAgain) {
      fetchAgain = false;
      void refresh();
    } else {
      poll = window.setTimeout(() => void refresh(), 3000);
    }
  }
}

async function stop(jobId: string) {
  killing.value = new Set(killing.value).add(jobId);
  try {
    await api.killBackgroundJob(props.conversationId, jobId);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    const next = new Set(killing.value);
    next.delete(jobId);
    killing.value = next;
  }
}

watch(
  () => props.count,
  () => void refresh(),
);
onMounted(() => {
  void refresh();
});
onBeforeUnmount(() => {
  disposed = true;
  window.clearTimeout(poll);
});
</script>
