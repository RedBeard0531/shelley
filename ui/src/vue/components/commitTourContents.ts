import type { GitTour, GitTourEntry, GitTourHeaderEntry } from "../../services/api";
import { analyzeTourPatch } from "./commitTourPatch";

export const TOUR_OVERVIEW_ANCHOR = "tour-overview";

export interface TourOverviewItem {
  anchor: string;
  label: string;
  kind: "overview";
}

export interface TourSectionItem {
  anchor: string;
  label: string;
  kind: "section";
}

export interface TourChangeItem {
  anchor: string;
  label: string;
  kind: "change";
  treePath: string[];
  decoration?: string;
  decorationTitle?: string;
}

export type TourContentsItem = TourOverviewItem | TourSectionItem | TourChangeItem;

export type TourContentsRow =
  | {
      kind: "directory";
      key: string;
      label: string;
    }
  | {
      kind: "change";
      key: string;
      item: TourChangeItem;
      filenameStem: string;
      filenameSuffix: string;
      depth: number;
    };

export interface TourContentsGroup {
  section: TourSectionItem | null;
  rows: TourContentsRow[];
}

export interface TourContentsLayout {
  overview: TourOverviewItem | null;
  groups: TourContentsGroup[];
}

export function tourEntryAnchor(position: number): string {
  return `tour-entry-${position}`;
}

function isHeaderEntry(entry: GitTourEntry): entry is GitTourHeaderEntry {
  return "header" in entry;
}

function headerLabel(markdown: string): string {
  const firstLine = markdown
    .split(/\r?\n/)
    .map((line) => line.trim())
    .find(Boolean);
  if (!firstLine) return "Section";
  return firstLine
    .replace(/^#{1,6}\s*/, "")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/[*_`~]/g, "")
    .trim();
}

export function buildTourContents(tour: GitTour, includeOverview: boolean): TourContentsItem[] {
  const contents: TourContentsItem[] = [];
  if (includeOverview || tour.title || tour.intro) {
    contents.push({ anchor: TOUR_OVERVIEW_ANCHOR, label: "Overview", kind: "overview" });
  }

  tour.chunks.forEach((entry, position) => {
    if (isHeaderEntry(entry)) {
      contents.push({
        anchor: tourEntryAnchor(position),
        label: headerLabel(entry.header),
        kind: "section",
      });
      return;
    }
    const patch = analyzeTourPatch(entry.patch);
    const decoration = patch.displayRange
      ? `L${patch.displayRange[0]}${patch.displayRange[0] === patch.displayRange[1] ? "" : `–${patch.displayRange[1]}`}`
      : undefined;
    contents.push({
      anchor: tourEntryAnchor(position),
      label: patch.label,
      kind: "change",
      treePath: patch.fileLabel.split("/"),
      decoration,
      decorationTitle: patch.displayRange
        ? `lines ${patch.displayRange[0]}–${patch.displayRange[1]}`
        : undefined,
    });
  });
  return contents;
}

export function buildTourContentsLayout(items: TourContentsItem[]): TourContentsLayout {
  const overview = items.find((item): item is TourOverviewItem => item.kind === "overview") ?? null;
  const groups: TourContentsGroup[] = [];
  let current: TourContentsGroup = { section: null, rows: [] };
  let previousDirectory = "";

  const finishGroup = () => {
    if (current.section || current.rows.length > 0) groups.push(current);
  };

  for (const item of items) {
    if (item.kind === "overview") continue;
    if (item.kind === "section") {
      finishGroup();
      current = { section: item, rows: [] };
      previousDirectory = "";
      continue;
    }

    const directories = item.treePath.slice(0, -1);
    const directory = directories.join("\0");
    if (directories.length > 0 && directory !== previousDirectory) {
      current.rows.push({
        kind: "directory",
        key: `directory:${item.anchor}`,
        label: directories.join(" / "),
      });
    }
    previousDirectory = directory;

    const filename = item.treePath.at(-1) || item.label;
    const lastDot = filename.lastIndexOf(".");
    const previousDot = lastDot > 0 ? filename.lastIndexOf(".", lastDot - 1) : -1;
    const suffixStart = previousDot > 0 ? previousDot : lastDot;
    current.rows.push({
      kind: "change",
      key: item.anchor,
      item,
      filenameStem: suffixStart > 0 ? filename.slice(0, suffixStart) : filename,
      filenameSuffix: suffixStart > 0 ? filename.slice(suffixStart) : "",
      depth: directories.length > 0 ? 1 : 0,
    });
  }
  finishGroup();

  return { overview, groups };
}
