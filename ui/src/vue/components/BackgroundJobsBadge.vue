<!-- Drawer-row badge counting a conversation's running background bash jobs.
     Clicking it opens a popover listing the jobs, each with a Kill button.
     The count comes from the conversation list stream; while the popover is
     open, a change in the count re-fetches the list. It uses a native title
     rather than v-tooltip: PrimeVue's tooltip stays up over the open popover. -->
<template>
  <button
    type="button"
    class="conversation-background-jobs-badge"
    data-testid="background-jobs-badge"
    :title="open ? undefined : label"
    :aria-label="label"
    :aria-expanded="open"
    aria-haspopup="dialog"
    @click.stop="toggle"
    @auxclick.stop
  >
    <svg
      class="conversation-background-jobs-icon"
      fill="none"
      stroke="currentColor"
      viewBox="0 0 24 24"
      aria-hidden="true"
    >
      <path
        stroke-linecap="round"
        stroke-linejoin="round"
        :stroke-width="2"
        d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
      />
      <path
        stroke-linecap="round"
        stroke-linejoin="round"
        :stroke-width="2"
        d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
      />
    </svg>
    {{ count }}
  </button>
  <Popover
    ref="popoverRef"
    :pt="{
      root: { class: 'background-jobs-popover', 'aria-label': 'Background jobs' },
      content: { class: 'background-jobs-popover-content' },
    }"
    @show="onShow"
    @hide="onHide"
  >
    <div class="background-jobs-title">Background jobs</div>
    <div v-if="error" class="background-jobs-error" role="alert">{{ error }}</div>
    <div v-if="!jobs" class="background-jobs-empty">{{ error ? "" : "Loading…" }}</div>
    <div v-else-if="jobs.length === 0" class="background-jobs-empty">No running jobs.</div>
    <ul v-else class="background-jobs-list">
      <li
        v-for="job in jobs"
        :key="job.job_id"
        class="background-jobs-item"
        data-testid="background-job"
      >
        <div class="background-jobs-command" :title="job.command">{{ job.command }}</div>
        <div class="background-jobs-meta">
          <span>{{ job.job_id }}</span>
          <span>{{ elapsedSince(job.started_at, now) }}</span>
          <span>PGID {{ job.pgid }}</span>
        </div>
        <div class="background-jobs-log" :title="job.log_path">{{ job.log_path }}</div>
        <Button
          class="background-jobs-kill"
          data-testid="background-job-kill"
          size="small"
          severity="danger"
          outlined
          :disabled="killing.has(job.job_id)"
          :label="killing.has(job.job_id) ? 'Killing…' : 'Kill'"
          :aria-label="`Kill background job ${job.job_id}`"
          @click="kill(job.job_id)"
        />
      </li>
    </ul>
  </Popover>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import Button from "primevue/button";
import Popover from "primevue/popover";
import { api, type BackgroundJob } from "../../services/api";
import { elapsedSince } from "./tools/toolElapsed";

const props = defineProps<{ conversationId: string; count: number }>();

const label = computed(
  () => `${props.count} background job${props.count === 1 ? "" : "s"} running`,
);
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const open = ref(false);
const jobs = ref<BackgroundJob[] | null>(null);
const error = ref("");
const killing = ref(new Set<string>());
const now = ref(Date.now());
let clock: number | undefined;
let fetchSeq = 0;

function toggle(event: MouseEvent) {
  popoverRef.value?.toggle(event);
}

async function refresh() {
  const seq = ++fetchSeq;
  try {
    const list = await api.getBackgroundJobs(props.conversationId);
    if (seq !== fetchSeq) return;
    jobs.value = list;
    error.value = "";
    killing.value = new Set([...killing.value].filter((id) => list.some((j) => j.job_id === id)));
  } catch (e) {
    if (seq === fetchSeq) error.value = e instanceof Error ? e.message : String(e);
  }
}

function onShow() {
  open.value = true;
  now.value = Date.now();
  clock = window.setInterval(() => (now.value = Date.now()), 1000);
  void refresh();
}

function onHide() {
  open.value = false;
  window.clearInterval(clock);
  jobs.value = null;
  error.value = "";
}

async function kill(jobId: string) {
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

// The list stream updates the count when a job starts or exits.
watch(
  () => props.count,
  (count) => {
    if (!open.value) return;
    if (count === 0) popoverRef.value?.hide();
    else void refresh();
  },
);

onBeforeUnmount(() => window.clearInterval(clock));
</script>
