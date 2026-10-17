// paint.ts — the shell's root-paint rule: one render on the stack at a time. A paint answered
// while another runs (the radar controller's flush reaching its listener mid-render, from an
// effect inside the tree) must not re-enter preact's render: the render in flight has already
// claimed the root, so the nested one diffs its tree against nothing and appends a second whole
// app beside the first. The nested call queues one trailing paint instead, which runs when the
// stack unwinds and repaints the newest state once.

/** Wraps the shell's paint body so it never runs on the stack twice. Calls that arrive while a
 *  paint is in flight are coalesced into one trailing paint, on the next microtask. */
export function paintGuard(body: () => void): () => void {
  let painting = false;
  let queued = false;
  const paint = (): void => {
    if (painting) {
      if (queued) return;
      queued = true;
      queueMicrotask(() => {
        queued = false;
        // through the guard: the trailing paint can meet a radar flush of its own
        paint();
      });
      return;
    }
    painting = true;
    try {
      body();
    } finally {
      painting = false;
    }
  };
  return paint;
}
