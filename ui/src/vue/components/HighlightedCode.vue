<!-- A plain element until the shared Shiki worker returns structured tokens.
     Keeping the source as a Vue text child means command text is never HTML. -->
<template>
  <component :is="tag" ref="codeRef">{{ source }}</component>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { highlightCode } from "../../services/markdownHighlight";
import { applyHighlightTokens } from "../../utils/codeHighlight";

const props = withDefaults(
  defineProps<{
    source: string;
    language: string;
    tag?: "pre" | "span";
  }>(),
  { tag: "span" },
);

const codeRef = ref<HTMLElement | null>(null);

watch(
  [() => props.source, () => props.language, codeRef],
  () => {
    const code = codeRef.value;
    const source = props.source;
    const language = props.language;
    if (!code) return;

    const requestState = `pending:${language}\0${source}`;
    code.dataset.shelleyCodeHighlight = requestState;
    // Reset the element's text to the new source. Vue's vdom still tracks the
    // text node from the initial render, but applyHighlightTokens replaced the
    // element's children, so Vue would patch a detached node and leave stale
    // text visible. Writing textContent here keeps the DOM authoritative.
    code.textContent = source;
    void highlightCode(language, source)
      .then((result) => {
        // The command can change while the worker is tokenizing. Do not let an
        // older response replace newer text (or an unmounted element).
        if (
          !code.isConnected ||
          code.dataset.shelleyCodeHighlight !== requestState ||
          code.textContent !== source
        )
          return;
        if (result.kind === "unknown") {
          delete code.dataset.shelleyCodeHighlight;
          return;
        }
        applyHighlightTokens(code, source, result.lines);
        code.dataset.shelleyCodeHighlight = `highlighted:${language}\0${source}`;
      })
      .catch((error: unknown) => {
        if (code.dataset.shelleyCodeHighlight === requestState) {
          delete code.dataset.shelleyCodeHighlight;
        }
        console.error("Syntax highlighting failed", error);
      });
  },
  { flush: "post", immediate: true },
);
</script>
