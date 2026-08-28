import { JSDOM } from "jsdom";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { pushBackButtonDismiss, popBackButtonDismiss } from "./backButtonDismiss";
import { MODAL_OVERLAY_CLASSES } from "./mobileDrawerSwipe";

let failed = 0;
function assert(cond: boolean, msg: string): void {
  if (!cond) {
    failed++;
    console.error(`    ✗ ${msg}`);
  } else {
    console.log(`    ✓ ${msg}`);
  }
}

const dom = new JSDOM("<!doctype html><html><body></body></html>", {
  url: "https://shelley.example/c/the-slug",
});
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
});

// Drive the same path a real back button press takes: pop one history entry,
// which fires a (deferred) "popstate" event on window.
function pressBack(): Promise<void> {
  dom.window.history.back();
  // jsdom delivers popstate on a later task; let it run.
  return new Promise((r) => setTimeout(r, 10));
}

const closed: string[] = [];
const open = (name: string) => () => closed.push(name);

async function main() {
  // --- Stacking: back closes the topmost overlay only ---
  pushBackButtonDismiss(open("git"));
  pushBackButtonDismiss(open("diff"));
  await pressBack();
  await pressBack();
  await pressBack(); // stack now empty; back must not close anything
  assert(
    JSON.stringify(closed) === '["diff","git"]',
    "back closes topmost first, then the next, then nothing when empty",
  );

  // --- Manual close removes only its own callback ---
  closed.length = 0;
  const picker = open("picker");
  const other = open("other");
  pushBackButtonDismiss(picker);
  pushBackButtonDismiss(other);
  popBackButtonDismiss(picker); // e.g. ESC on the bottom-most overlay
  await pressBack();
  assert(JSON.stringify(closed) === '["other"]', "manual close only deregisters that overlay");

  // --- Every fullscreen overlay registers back-button dismissal ---
  // The overlays that carry their own chrome (rather than PrimeVue's Dialog via
  // Modal.vue) are the ones that forget: the file editor and the image
  // annotator were both missed the first time. Any component rendering an
  // overlay of its own must name it in this module.
  const componentsDir = fileURLToPath(new URL("../components/", import.meta.url));
  const overlays = readdirSync(componentsDir)
    .filter((name) => name.endsWith(".vue"))
    .map((name) => [name, readFileSync(join(componentsDir, name), "utf8")] as const)
    .filter(
      ([, src]) =>
        // The attribute as written in a template; MessageInput only mentions
        // '[aria-modal="true"]' in a selector and is not an overlay itself.
        /[^'"[]aria-modal="true"/.test(src) ||
        MODAL_OVERLAY_CLASSES.some((cls) => src.includes(`"${cls}"`)),
    )
    .map(([name]) => name);
  assert(overlays.length > 0, "the overlay scan finds the overlay components");
  for (const name of overlays) {
    const src = readFileSync(join(componentsDir, name), "utf8");
    assert(src.includes("pushBackButtonDismiss"), `${name} wires up back-button dismissal`);
  }
}

main().finally(() => {
  if (failed > 0) process.exitCode = 1;
});
