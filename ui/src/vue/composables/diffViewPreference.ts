import { readonly, ref } from "vue";

// Both layout preferences are one persisted value with a viewport-based default;
// they differ only in how the stored string parses.
function storedPreference<T>(
  key: string,
  parse: (stored: string) => T | undefined,
  fallback: () => T,
): T {
  try {
    const stored = localStorage.getItem(key);
    if (stored !== null) {
      const parsed = parse(stored);
      if (parsed !== undefined) return parsed;
    }
  } catch {
    // Use the viewport-based default when storage is unavailable.
  }
  return fallback();
}

function persistPreference(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // The in-memory preference still applies when storage is unavailable.
  }
}

export const SIDE_BY_SIDE_STORAGE_KEY = "shelley-diff-side-by-side";

const sideBySidePreference = ref(
  storedPreference(
    SIDE_BY_SIDE_STORAGE_KEY,
    (stored) => stored === "true",
    () => {
      return window.innerWidth >= 768;
    },
  ),
);

export function setSideBySidePreference(value: boolean): void {
  sideBySidePreference.value = value;
  persistPreference(SIDE_BY_SIDE_STORAGE_KEY, value ? "true" : "false");
}

export function useSideBySidePreference() {
  return {
    sideBySidePreference: readonly(sideBySidePreference),
    setSideBySidePreference,
  };
}

export type DiffOverflow = "scroll" | "wrap";

export const OVERFLOW_STORAGE_KEY = "shelley-diff-overflow";

const overflowPreference = ref<DiffOverflow>(
  storedPreference(
    OVERFLOW_STORAGE_KEY,
    (stored) => (stored === "wrap" || stored === "scroll" ? stored : undefined),
    // Long lines read better wrapped on narrow viewports.
    () => (window.innerWidth >= 768 ? "scroll" : "wrap"),
  ),
);

export function setOverflowPreference(value: DiffOverflow): void {
  overflowPreference.value = value;
  persistPreference(OVERFLOW_STORAGE_KEY, value);
}

export function useOverflowPreference() {
  return {
    overflowPreference: readonly(overflowPreference),
    setOverflowPreference,
  };
}
