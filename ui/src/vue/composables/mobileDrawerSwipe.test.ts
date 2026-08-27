import { JSDOM } from "jsdom";
import {
  eventPathHasHorizontalScrollContainer,
  hasHorizontalScrollContainer,
  isPopupTarget,
} from "./mobileDrawerSwipe";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

const dom = new JSDOM("<main><div id='wide'><code id='target'></code></div></main>");
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Element: dom.window.Element,
});

const wide = document.querySelector("#wide") as HTMLElement;
const target = document.querySelector("#target") as HTMLElement;

Object.defineProperties(wide, {
  clientWidth: { value: 320, configurable: true },
  scrollWidth: { value: 640, configurable: true },
});

run("detects a horizontally scrollable ancestor", () => {
  wide.style.overflowX = "auto";
  assert(hasHorizontalScrollContainer(target), "wide code content should own the swipe");
});

run("ignores an ancestor whose content fits", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 320, configurable: true });
  assert(
    !hasHorizontalScrollContainer(target),
    "non-overflowing content should allow drawer swipes",
  );
});

run("ignores clipped overflow", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 640, configurable: true });
  wide.style.overflowX = "hidden";
  assert(!hasHorizontalScrollContainer(target), "hidden overflow is not horizontally scrollable");
});

// Popup targets: full-screen overlays (diff viewer, git graph, image
// comments, command palette) and aria-modal dialogs are mounted inside the
// app shell, so drawer gestures must not start on them.
const popupDom = new JSDOM(`
  <div class="app-container">
    <div class="drawer" id="drawer"></div>
    <div class="main-content">
      <div class="diff-viewer-overlay"><div id="in-overlay"></div></div>
      <div class="command-palette-overlay"><div id="in-palette"></div></div>
      <div aria-modal="true"><div id="in-dialog"></div></div>
      <div id="plain-content"></div>
    </div>
  </div>
`);

const overlayTarget = popupDom.window.document.querySelector("#in-overlay") as HTMLElement;
const paletteTarget = popupDom.window.document.querySelector("#in-palette") as HTMLElement;
const dialogTarget = popupDom.window.document.querySelector("#in-dialog") as HTMLElement;
const plainTarget = popupDom.window.document.querySelector("#plain-content") as HTMLElement;
const drawerTarget = popupDom.window.document.querySelector("#drawer") as HTMLElement;

run("treats -overlay popups as drawer-swipe-free zones", () => {
  assert(isPopupTarget(overlayTarget), "swipe inside an overlay should not drive the drawer");
  assert(isPopupTarget(paletteTarget), "command palette overlay should also own swipes");
});

run("treats aria-modal dialogs as drawer-swipe-free zones", () => {
  assert(isPopupTarget(dialogTarget), "swipe inside an aria-modal dialog should not drive the drawer");
});

run("leaves non-popup content and the open drawer swipable", () => {
  assert(!isPopupTarget(plainTarget), "plain content should allow drawer swipes");
  assert(!isPopupTarget(drawerTarget), "swiping the open drawer should still close it");
  assert(!isPopupTarget(null), "null target should never be a popup");
});

// composed-path scan: document-level touch handlers see event.target
// retargeted to the shadow host, so the old parentElement walk could never
// descend into a shadow root. composedPath() includes those nodes — that's
// how the @pierre/diffs tool-card diff's [data-code] scroll container (which
// lives in a shadow root) gets to own the swipe.
run("composedPath reaches a scroll container inside a shadow root", () => {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const shadow = host.attachShadow({ mode: "open" });
  shadow.innerHTML = `<div id="code"><span id="inner">wide line</span></div>`;
  const code = shadow.querySelector("#code") as HTMLElement;
  const inner = shadow.querySelector("#inner") as HTMLElement;

  // A document-level listener sees the composed path, not just the host.
  let path: EventTarget[] = [];
  document.addEventListener("drawer-swipe-test", (e) => (path = e.composedPath()));
  inner.dispatchEvent(
    new document.defaultView!.Event("drawer-swipe-test", { bubbles: true, composed: true }),
  );
  document.removeEventListener("drawer-swipe-test", () => {});
  assert(path.includes(code), "the composed path should include shadow-tree nodes");
  assert(path.includes(host), "the composed path should include the shadow host");
  // (jsdom pierces composedPath but cannot apply computed styles inside shadow
  // roots, so the wide-scroll predicate itself is exercised in light DOM via
  // the same path scan below.)
});

run("path scan finds a wide scroll container on the composed path", () => {
  wide.style.overflowX = "auto";
  Object.defineProperty(wide, "scrollWidth", { value: 640, configurable: true });
  let path: EventTarget[] = [];
  document.addEventListener("drawer-swipe-test-wide", (e) => (path = e.composedPath()));
  target.dispatchEvent(
    new document.defaultView!.Event("drawer-swipe-test-wide", { bubbles: true, composed: true }),
  );
  document.removeEventListener("drawer-swipe-test-wide", () => {});
  assert(
    eventPathHasHorizontalScrollContainer(path),
    "a wide scroll container on the composed path should own the swipe",
  );
});

run("path scan ignores fitting scroll containers", () => {
  wide.style.overflowX = "auto";
  Object.defineProperty(wide, "scrollWidth", { value: 320, configurable: true });
  let path: EventTarget[] = [];
  document.addEventListener("drawer-swipe-test-fit", (e) => (path = e.composedPath()));
  target.dispatchEvent(
    new document.defaultView!.Event("drawer-swipe-test-fit", { bubbles: true, composed: true }),
  );
  document.removeEventListener("drawer-swipe-test-fit", () => {});
  assert(
    !eventPathHasHorizontalScrollContainer(path),
    "a fitting scroll container should not block drawer swipes",
  );
});

console.log("\nmobileDrawerSwipe tests passed");
