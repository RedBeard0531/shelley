import { JSDOM } from "jsdom";
import {
  pushBackButtonDismiss,
  popBackButtonDismiss,
} from "./backButtonDismiss";

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
}

main().finally(() => {
  if (failed > 0) process.exitCode = 1;
});
