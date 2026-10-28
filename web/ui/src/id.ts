// id.ts — ids that stay unique however late a component mounts. Preact's useId counts on a mask
// that travels between renders on the tree's wrapper vnodes, and a render that finds no mask on
// the last one starts a fresh count, repeating an id the page already serves. A module counter
// has no paint to lose.

import { useState } from "preact/hooks";

let next = 0;

/** An id made once per mounted component, stable across re-renders and unique on the page. */
export function useUiId(prefix: string): string {
  return useState(() => `${prefix}-${next++}`)[0];
}
