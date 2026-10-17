import { vi } from "vite-plus/test";

/**
 * Replace window.matchMedia with a stub whose `matches` answers per query, so a media-query-driven
 * layout (the phone tab bar, reduced motion) is decided by the test, not by the host window.
 */
export function stubMatchMedia(matches: (query: string) => boolean): void {
  vi.stubGlobal(
    "matchMedia",
    (query: string): MediaQueryList =>
      ({
        matches: matches(query),
        media: query,
        onchange: null,
        addListener: () => undefined,
        removeListener: () => undefined,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        dispatchEvent: () => false,
      }) as unknown as MediaQueryList,
  );
}
