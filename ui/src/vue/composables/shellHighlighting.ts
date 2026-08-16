import { ref, watchEffect } from "vue";
import { loadHighlightModule } from "../../utils/markdownRender";

// Shell commands are tokenized lazily via the shared /markdown-highlight.js
// chunk. Cache results by command text: commands rarely change per card, and
// large conversations otherwise re-tokenize identical noisy commands on every
// render of the same card.
const highlightedCommandCache = new Map<string, string | null>();
const HIGHLIGHTED_COMMAND_CACHE_MAX = 512;

function rememberHighlightedCommand(command: string, html: string | null): void {
  // Simple insertion-order cap: commands are cheap to re-tokenize later, and
  // an unbounded cache would grow with every distinct command in a long session.
  if (highlightedCommandCache.size >= HIGHLIGHTED_COMMAND_CACHE_MAX) {
    const oldest = highlightedCommandCache.keys().next().value;
    if (oldest !== undefined) highlightedCommandCache.delete(oldest);
  }
  highlightedCommandCache.set(command, html);
}

async function highlightShellCommandCached(command: string): Promise<string | null> {
  const cached = highlightedCommandCache.get(command);
  if (cached !== undefined) return cached;

  const module = await loadHighlightModule();
  let html: string | null = null;
  if (module?.highlightShellCommand) {
    try {
      html = module.highlightShellCommand(command);
    } catch (err) {
      console.error("failed to highlight shell command:", err);
    }
  }
  rememberHighlightedCommand(command, html);
  return html;
}

/**
 * Returns highlighted shell-command HTML for the current `source` value,
 * falling back to null until the lazy chunk loads (callers render plain text
 * meanwhile). Cancels stale updates when the source changes before a pending
 * highlight resolves.
 */
export function useShellCommandHighlighting(source: () => string) {
  const highlighted = ref<string | null>(null);

  watchEffect((onCleanup) => {
    let stale = false;
    onCleanup(() => {
      stale = true;
    });

    highlightShellCommandCached(source()).then((html) => {
      if (!stale) highlighted.value = html;
    });
  });

  return highlighted;
}
