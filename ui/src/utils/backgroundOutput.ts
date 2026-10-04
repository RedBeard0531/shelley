// The bash tool result includes instructions for the model after the command's
// output. Use the structured job metadata to remove that generated trailer
// from the UI, without editing the message the model receives.
export interface BackgroundJobDisplay {
  jobId: string;
  logPath: string;
  pgid: number;
}

export function backgroundOutput(text: string, job: BackgroundJobDisplay): string {
  const trailer =
    ` background job ${job.jobId} (PGID ${job.pgid}). Log: ${job.logPath}\n` +
    "Completion will wake this conversation; do not poll or sleep waiting for it. " +
    "Keep working on other things, or end your turn if nothing remains. " +
    `Cancel with \`kill -- -${job.pgid}\`.]`;
  // An unrecognized trailer may contain IDs or instructions for the model.
  // Fail closed rather than showing it as command output.
  if (!text.endsWith(trailer)) return "";

  const prefix = text.slice(0, -trailer.length);
  const start = /(?:^|\n)\[(?:Started as|Still running after [^\n]+; moved to)$/.exec(prefix);
  if (!start) return "";
  return prefix.slice(0, start.index).replace(/\n$/, "");
}
