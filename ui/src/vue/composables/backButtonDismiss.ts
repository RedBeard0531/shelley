// Lets the browser back button close the topmost open modal/overlay (git graph,
// diff viewer, dialogs, pickers, ...) instead of leaving the page. Mirrors
// modalEscapeStack: each opener registers a close callback and back only ever
// dismisses the topmost, so stacked overlays close one at a time.
//
// Each open pushes a history entry (sharing the current URL), which gives back
// a popstate to intercept. When nothing is open the shared listener does
// nothing, so back navigates normally (the app's own popstate handler in
// App.vue then handles conversation switching). We never history.back() on
// manual close: the leftover same-URL entries are harmless because we only ever
// react to back while the stack is non-empty.

const stack: Array<() => void> = [];

function onPopstate(this: Window) {
  if (stack.length === 0) return;
  // Back was pressed with an overlay open: close just the topmost one. We do
  // not re-push here; this popstate has already consumed the entry, so the
  // next back will either close the next overlay or (once empty) navigate.
  stack.pop()!();
}

let listening = false;

export function pushBackButtonDismiss(close: () => void): void {
  if (!listening) {
    window.addEventListener("popstate", onPopstate);
    listening = true;
  }
  // One entry per open overlay so back closes exactly one per press.
  window.history.pushState({ __shelleyBackButton: true }, "");
  stack.push(close);
}

export function popBackButtonDismiss(close: () => void): void {
  const i = stack.lastIndexOf(close);
  if (i !== -1) stack.splice(i, 1);
}
