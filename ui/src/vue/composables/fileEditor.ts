// Opening an arbitrary file in the standalone editor modal, from anywhere in
// the tree.
//
// App owns the EditableFileModal (one instance, so two cards can't fight over
// the same Monaco model) and provides this opener; components deep in the
// message list — e.g. a patch tool card's "open in editor" button — inject it
// instead of emitting through every intermediate component.
import { inject, provide, type InjectionKey } from "vue";

/** Options for opening a file at a specific location. */
export interface OpenFileOptions {
  /** 1-based line to reveal (and place the cursor on). */
  line?: number;
  /** Last line of the selection; only set for line-range references. */
  endLine?: number;
  /** Directory to resolve a relative path against (the emitting message's
   *  cwd). Falls back to the conversation's current cwd when absent. */
  baseDir?: string;
}

/** Opens `path` (absolute, or relative to the conversation's cwd) in the
 *  editor modal, optionally at a specific line/range. A path that can't be
 *  resolved is ignored. */
export type OpenFileEditor = (path: string, opts?: OpenFileOptions) => void;

export const OpenFileEditorKey: InjectionKey<OpenFileEditor> = Symbol("open-file-editor");

export function provideOpenFileEditor(open: OpenFileEditor): void {
  provide(OpenFileEditorKey, open);
}

/** The editor opener. App is the only host that mounts tool cards, and it
 *  always provides one; a missing provider is a bug (Vue logs the failed
 *  injection and calling this throws) rather than something to fall back for. */
export function useOpenFileEditor(): OpenFileEditor {
  return inject(OpenFileEditorKey) as OpenFileEditor;
}
