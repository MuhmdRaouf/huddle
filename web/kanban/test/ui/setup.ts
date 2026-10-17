import { cleanup } from "@testing-library/preact";
import { afterEach, vi } from "vite-plus/test";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
