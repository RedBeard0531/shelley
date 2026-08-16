// Touch swipe gestures for the conversation drawer on mobile:
//   swipe right  -> open the drawer
//   swipe left   -> close the drawer
//
// The drawer follows the finger live and snaps on release: past the midpoint
// (or flung in that direction) it continues out; otherwise it springs back.
// Active only below 768px, where the drawer is a fixed overlay. Once a drag is
// recognized as horizontal it takes over from the browser (preventing text
// selection / sideways scroll), so the drawer is "pulled" by the finger;
// vertical drags and taps are left alone, and drags starting on interactive
// controls — or while a long-press text selection is in progress — are
// declined so buttons/inputs/code editors and selection stay usable.
import { onBeforeUnmount, onMounted, type Ref } from "vue";

const MOBILE_MQ = "(max-width: 767px)";
const COMMIT_PX = 8; // horizontal dominance needed to take over
const DECIDE_PX = 50; // finger travel that commits a direction (matches tap-to-open feel)
const VELOCITY = 0.5; // px/ms — fling threshold (tie-breaker for short swipes)
const DURATION = 300; // ms snap animation

function isInside(el: Element | null, pred: (n: Element) => boolean): boolean {
  let node = el;
  while (node && node !== document.body) {
    if (pred(node)) return true;
    node = node.parentElement;
  }
  return false;
}

const editable = (n: Element): boolean =>
  n instanceof HTMLInputElement ||
  n instanceof HTMLTextAreaElement ||
  n instanceof HTMLSelectElement ||
  (n as HTMLElement).isContentEditable === true;

// Don't let a swipe-to-open yank focus from buttons/links/code editors.
const interactive = (n: Element): boolean =>
  editable(n) ||
  n instanceof HTMLButtonElement ||
  n instanceof HTMLAnchorElement ||
  n.classList.contains("monaco-editor");

const horizontallyScrollable = (el: Element | null): boolean =>
  isInside(el, (n) => {
    const ox = getComputedStyle(n).overflowX;
    return (ox === "auto" || ox === "scroll") && n.scrollWidth > n.clientWidth + 1;
  });

function selectionText(): string {
  return window.getSelection()?.toString() ?? "";
}

export function useDrawerSwipe(drawerOpen: Ref<boolean>) {
  const mq = window.matchMedia(MOBILE_MQ);
  let originX = 0; // touchstart X — drives the open/close decision
  let startX = 0; // commit-anchor X — the finger "owns" the drawer from here
  let startY = 0;
  let startTarget: Element | null = null;
  let wasOpen = false;
  let initialSelection = "";
  let width = 0;
  let baseX = 0; // drawer translateX when the drag committed (live, not resting)
  let tracking = false;
  let dragging = false;
  let lastX = 0;
  let lastT = 0;
  let prevX = 0;
  let prevT = 0;
  let snapTimer: number | undefined;
  // Cached at drag commit so the per-frame drag path stays off the DOM.
  let drawer: HTMLElement | null = null;
  let backdrop: HTMLElement | null = null;

  // Live position during the drag (no transition).
  function dragTo(px: number) {
    if (drawer) {
      drawer.style.transition = "none";
      drawer.style.transform = `translateX(${px}px)`;
    }
    const p = width > 0 ? (px + width) / width : 0; // 0 closed .. 1 open
    if (backdrop) {
      backdrop.style.transition = "none";
      backdrop.style.opacity = String(Math.max(0, Math.min(1, p)));
    }
  }

  // Animate to a resting state on release/cancel.
  function snap(open: boolean) {
    if (!drawer) return;
    const targetPx = open ? 0 : -width;
    drawer.style.transition = `transform ${DURATION}ms ease`;
    if (backdrop) backdrop.style.transition = `opacity ${DURATION}ms ease`;
    void drawer.offsetWidth; // reflow so the transition catches the change
    drawer.style.transform = `translateX(${targetPx}px)`;
    if (backdrop) backdrop.style.opacity = open ? "1" : "0";
    drawerOpen.value = open;
    window.clearTimeout(snapTimer);
    snapTimer = window.setTimeout(clearInline, DURATION + 20);
  }

  // Hand transform/opacity back to the class-driven CSS.
  function clearInline() {
    snapTimer = undefined;
    if (drawer) {
      drawer.style.transition = "";
      drawer.style.transform = "";
    }
    if (backdrop) {
      backdrop.style.transition = "";
      backdrop.style.opacity = "";
    }
  }

  function reset() {
    tracking = false;
    dragging = false;
  }

  // A drag's release point often generates a synthetic click. If it lands on
  // the drawer, backdrop, or header it would toggle the drawer right after the
  // swipe — a close→open / open→close flicker. Swallow that one click.
  function swallowNextDrawerClick() {
    const isDrawerToggle = (target: EventTarget | null): boolean => {
      if (!(target instanceof Element)) return false;
      return isInside(target, (n) =>
        n.classList.contains("drawer") ||
        n.classList.contains("backdrop") ||
        n.classList.contains("header"),
      );
    };
    const cleanup = () => {
      document.removeEventListener("click", swallow, true);
      window.clearTimeout(timer);
    };
    const swallow = (e: MouseEvent) => {
      if (isDrawerToggle(e.target)) {
        e.preventDefault();
        e.stopPropagation();
      }
      cleanup();
    };
    const timer = window.setTimeout(cleanup, 300);
    document.addEventListener("click", swallow, true);
  }

  function onTouchStart(e: TouchEvent) {
    // A second finger (or leaving mobile) abandons an in-progress drag
    // cleanly back to its pre-drag state instead of freezing it.
    if (!mq.matches || e.touches.length !== 1) {
      if (dragging) snap(wasOpen);
      reset();
      return;
    }
    const t = e.touches[0];
    const now = performance.now();
    originX = t.clientX;
    startX = t.clientX;
    startY = t.clientY;
    lastX = t.clientX;
    lastT = now;
    prevX = t.clientX;
    prevT = now;
    startTarget = t.target instanceof Element ? t.target : null;
    wasOpen = drawerOpen.value;
    initialSelection = selectionText();
    tracking = true;
    dragging = false;
  }

  function onTouchMove(e: TouchEvent) {
    if (!tracking) return;
    const t = e.touches[0];
    const dx = t.clientX - startX;
    const dy = t.clientY - startY;
    if (!dragging) {
      if (Math.abs(dx) < COMMIT_PX && Math.abs(dy) < COMMIT_PX) return;
      if (Math.abs(dy) >= Math.abs(dx)) {
        reset(); // vertical — let the page scroll
        return;
      }
      if (selectionText() !== initialSelection) {
        reset(); // a long-press selection is being extended — defer to it
        return;
      }
      if (horizontallyScrollable(startTarget)) {
        reset(); // let code blocks scroll sideways
        return;
      }
      if (!wasOpen && isInside(startTarget, interactive)) {
        reset(); // don't hijack buttons/links/editors when opening
        return;
      }
      if (wasOpen && isInside(startTarget, editable)) {
        reset(); // don't close while typing in the drawer
        return;
      }
      const d = document.querySelector<HTMLElement>(".drawer");
      if (!d) {
        reset();
        return;
      }
      // Anchor the drag to the drawer's *current* visual position so grabbing
      // it mid-(CSS-)transition doesn't jump to the resting state.
      const rect = d.getBoundingClientRect();
      drawer = d;
      backdrop = document.querySelector<HTMLElement>(".backdrop");
      width = rect.width;
      baseX = rect.left; // == current translateX (drawer is fixed at left:0)
      startX = t.clientX; // the finger now owns the drawer from here
      startY = t.clientY;
      window.clearTimeout(snapTimer);
      dragging = true;
    }
    e.preventDefault();
    prevX = lastX;
    prevT = lastT;
    lastX = t.clientX;
    lastT = performance.now();
    dragTo(Math.max(-width, Math.min(0, baseX + (t.clientX - startX))));
  }

  function onTouchEnd(e: TouchEvent) {
    if (!tracking) return;
    const wasDragging = dragging;
    reset();
    if (!wasDragging) return; // tap or vertical scroll — leave default alone
    swallowNextDrawerClick();
    const t = e.changedTouches[0];
    const delta = (t ? t.clientX : lastX) - originX; // total finger travel
    const v = lastT !== prevT ? (lastX - prevX) / (lastT - prevT) : 0;
    const sdelta = Math.sign(delta);
    let target: boolean;
    if (Math.abs(delta) >= DECIDE_PX) {
      target = delta > 0; // traveled far enough in that direction -> commit
    } else if (Math.abs(v) > VELOCITY && (sdelta === 0 || Math.sign(v) === sdelta)) {
      target = v > 0; // short swipe but a clear fling in (agreeing) direction
    } else {
      target = wasOpen; // not enough travel -> spring back (pushed back in / cancel)
    }
    snap(target);
  }

  function onTouchCancel() {
    if (!tracking) return;
    const wasDragging = dragging;
    reset();
    if (wasDragging) snap(wasOpen); // spring back to where we started
  }

  onMounted(() => {
    document.addEventListener("touchstart", onTouchStart, { passive: true });
    // touchmove is non-passive so we can preventDefault once we take over.
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", onTouchEnd, { passive: true });
    document.addEventListener("touchcancel", onTouchCancel, { passive: true });
  });

  onBeforeUnmount(() => {
    document.removeEventListener("touchstart", onTouchStart);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchCancel);
    window.clearTimeout(snapTimer);
  });
}
