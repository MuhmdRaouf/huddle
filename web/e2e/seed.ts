// seed.ts — the usage load the seeded hub carries beside the five hand-written requests: twelve
// long-named models, one request each at the fixture's own twenty-five-hours-back instant, so
// the Usage view's screen-reader table grows wider than the tablet viewport and the layout gate
// can catch a page that scrolls sideways. The instant is the seeded spool line's own
// (@@NOW-90000000@@): always yesterday's date, so the load sits outside every trailing-24-hour
// window the overview counts on, inside the 7-day spool list, the 7-day blocks window and both
// usage windows, and inside yesterday's own 5-hour metering block, so no new block appears. The
// lines ride the seeded session, so the drill-down tables stay as they were.

/** One route line per model: tiny token counts, all answered, no cache. */
export const LOAD_MODELS: readonly string[] = [
  "qwen3.8-max-0115-preview",
  "deepseek-v4-pro-0614-longctx",
  "kimi-k3-turbo-128k-instruct",
  "minimax-m3-flash-0207-preview",
  "ernie-5.0-lite-1121-turbo-edition",
  "spark-desk-v3-25b-instruct-long",
  "hunyuan-large-20251104-preview",
  "doubao-pro-32k-function-call-1128",
  "step-2-16k-coder-instruct-final",
  "sensechat-v5.5-vision-0912-xlarge",
  "abab7-chat-preview-20260115-wide",
  "moonshot-r1-distill-0820-longthink",
];

/** The tokens one load request carries. */
export const LOAD_INPUT = 100;
export const LOAD_OUTPUT = 20;

/** The load's request count, and the totals it adds to the windows that cover its instant. */
export const LOAD_REQUESTS = LOAD_MODELS.length;
export const LOAD_TOKENS = {
  processed: LOAD_REQUESTS * (LOAD_INPUT + LOAD_OUTPUT),
  uncached: LOAD_REQUESTS * LOAD_INPUT,
  output: LOAD_REQUESTS * LOAD_OUTPUT,
} as const;

// the asks the needs-sheet cases seed through /api/needs: one Bash whose command cannot fit one
// line, one Edit whose change spans lines, so a preview has something to wrap and to diff.

/** A Bash command exactly 400 characters long: it must wrap in its code box, never scroll. */
export const ASK_COMMAND = `echo ${"a".repeat(387)} | wc -c`;

/** The Edit the diff cases seed: two changed lines between kept ones. */
export const ASK_EDIT = {
  file_path: "/w/now.ts",
  old_string: "const rate = 1;\nexport function read() {\n  return rate;\n}",
  new_string: "const rate = 2;\nexport function read() {\n  return rate * 2;\n}",
} as const;
