// Opening a commit in the diff viewer (the commit viewer), from anywhere in
// the tree.
//
// ChatInterface owns the DiffViewer and provides this opener; markdown
// commit-reference chips (`⎇hash`, see utils/markdownRender.ts) deep in the
// message list inject it instead of emitting through every intermediate
// component — the same arrangement as the file editor opener (fileEditor.ts).
import { provide, type InjectionKey } from "vue";

/** Opens `hash` (an abbreviated or full git commit hash) in the commit viewer
 *  for the conversation's current working directory. */
export type OpenCommitViewer = (hash: string) => void;

export const OpenCommitViewerKey: InjectionKey<OpenCommitViewer> = Symbol(
  "open-commit-viewer",
);

export function provideOpenCommitViewer(open: OpenCommitViewer): void {
  provide(OpenCommitViewerKey, open);
}
