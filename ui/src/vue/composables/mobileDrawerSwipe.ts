import { onMounted, onUnmounted, type Ref } from "vue";

const SYSTEM_EDGE_WIDTH = 32;
const SWIPE_DISTANCE = 48;
const DIRECTION_LOCK_DISTANCE = 10;
const HORIZONTAL_BIAS = 1.25;

// Full-screen popups (diff viewer, git graph, image comments, command
// palette, PrimeVue modals) sit inside the app shell's light DOM, so a
// drawer swipe that starts on one opens/closes the drawer underneath —
// invisible until the popup closes. While a popup is open it owns all
// touch gestures. Overlays in the app follow a "<name>-overlay" class
// convention; dialogs additionally/alternatively set aria-modal.
const POPUP_SELECTOR = '[aria-modal="true"], [class$="-overlay"]';

type Gesture = {
  startX: number;
  startY: number;
  lastX: number;
  lastY: number;
  opening: boolean;
  cancelled: boolean;
};

export function isPopupTarget(target: Element | null): boolean {
  return !!target?.closest(POPUP_SELECTOR);
}

function isHorizontalScrollContainer(element: Element): boolean {
  const style = window.getComputedStyle(element);
  return (
    (style.overflowX === "auto" || style.overflowX === "scroll") &&
    element.scrollWidth > element.clientWidth
  );
}

export function hasHorizontalScrollContainer(target: Element | null): boolean {
  for (let element = target; element; element = element.parentElement) {
    if (isHorizontalScrollContainer(element)) return true;
  }
  return false;
}

// The composed path pierces shadow roots, which document-level listeners
// otherwise never see: touch events retarget event.target to the shadow
// host, so a parentElement walk can't reach a scroll container rendered
// inside one (e.g. the @pierre/diffs tool-card diff, whose [data-code]
// scroll container lives in a shadow root). The path starts at the target,
// includes every shadow-tree node it crossed, then continues through the
// same light-DOM ancestors the walk above covers.
export function eventPathHasHorizontalScrollContainer(path: readonly EventTarget[]): boolean {
  return path.some(
    (node): node is Element => node instanceof Element && isHorizontalScrollContainer(node),
  );
}

export function useMobileDrawerSwipe(drawerOpen: Ref<boolean>) {
  let gesture: Gesture | null = null;

  function onTouchStart(event: TouchEvent) {
    if (!window.matchMedia("(max-width: 767px)").matches || event.touches.length !== 1) return;

    const touch = event.touches[0];
    const target = event.target instanceof Element ? event.target : null;
    const opening = !drawerOpen.value;

    // Popups own all gestures (see isPopupTarget) so a swipe on them can't
    // open or close the drawer underneath.
    if (isPopupTarget(target)) return;

    // Code blocks, tables, diffs, and other wide content own horizontal
    // gestures. Starting a drawer swipe there makes ordinary scrolling
    // unexpectedly navigate the app. composedPath() — not the retargeted
    // event.target — is what reaches scroll containers inside shadow DOM,
    // like the @pierre/diffs tool-card diff's [data-code] element.
    if (eventPathHasHorizontalScrollContainer(event.composedPath())) return;

    if (opening) {
      // Leave the true screen edge to the browser/OS back gesture.
      if (touch.clientX < SYSTEM_EDGE_WIDTH || !target?.closest(".main-content")) return;
    } else if (!target?.closest(".app-container")) {
      return;
    }

    gesture = {
      startX: touch.clientX,
      startY: touch.clientY,
      lastX: touch.clientX,
      lastY: touch.clientY,
      opening,
      cancelled: false,
    };
  }

  function onTouchMove(event: TouchEvent) {
    if (!gesture) return;
    if (event.touches.length !== 1) {
      gesture = null;
      return;
    }

    const touch = event.touches[0];
    gesture.lastX = touch.clientX;
    gesture.lastY = touch.clientY;

    const dx = touch.clientX - gesture.startX;
    const dy = touch.clientY - gesture.startY;
    const absX = Math.abs(dx);
    const absY = Math.abs(dy);

    if (Math.max(absX, absY) < DIRECTION_LOCK_DISTANCE) return;
    if (absY > absX) {
      gesture.cancelled = true;
      return;
    }

    const intendedDirection = gesture.opening ? dx > 0 : dx < 0;
    if (!gesture.cancelled && intendedDirection) event.preventDefault();
  }

  function finishGesture(event: TouchEvent) {
    if (!gesture) return;

    const touch = event.changedTouches[0];
    if (touch) {
      gesture.lastX = touch.clientX;
      gesture.lastY = touch.clientY;
    }

    const dx = gesture.lastX - gesture.startX;
    const dy = gesture.lastY - gesture.startY;
    const horizontal =
      Math.abs(dx) >= SWIPE_DISTANCE && Math.abs(dx) > Math.abs(dy) * HORIZONTAL_BIAS;

    if (!gesture.cancelled && horizontal) {
      if (gesture.opening && dx > 0) drawerOpen.value = true;
      if (!gesture.opening && dx < 0) drawerOpen.value = false;
    }

    gesture = null;
  }

  function cancelGesture() {
    gesture = null;
  }

  onMounted(() => {
    document.addEventListener("touchstart", onTouchStart, { passive: true });
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", finishGesture, { passive: true });
    document.addEventListener("touchcancel", cancelGesture, { passive: true });
  });

  onUnmounted(() => {
    document.removeEventListener("touchstart", onTouchStart);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", finishGesture);
    document.removeEventListener("touchcancel", cancelGesture);
  });
}
