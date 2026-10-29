// scan.ts — the design guard's pure parts: which class names legacy.css owns, how one file's text
// turns into offences under the six rules, and which sentences the ratchet answers with. No file
// access; the walk over web/hub/src lives in ../design-guard.test.ts.

export type Rule = "daisy" | "hr" | "legacy" | "colour" | "px" | "control";
export type Offence = { rule: Rule; line: number; text: string };
export type Counts = Record<string, Partial<Record<Rule, number>>>;

// daisyUI's bare component words; `btn-primary` is one of these plus a suffix
const DAISY_WORDS = new Set([
  "alert",
  "avatar",
  "badge",
  "btn",
  "card",
  "carousel",
  "chat",
  "checkbox",
  "countdown",
  "diff",
  "divider",
  "dock",
  "drawer",
  "dropdown",
  "fieldset",
  "file-input",
  "footer",
  "hero",
  "indicator",
  "input",
  "join",
  "kbd",
  "label",
  "link",
  "list",
  "loading",
  "menu",
  "mockup",
  "modal",
  "navbar",
  "progress",
  "radial-progress",
  "radio",
  "range",
  "rating",
  "select",
  "skeleton",
  "stack",
  "stat",
  "stats",
  "status",
  "steps",
  "swap",
  "tab",
  "tabs",
  "textarea",
  "timeline",
  "toast",
  "toggle",
  "tooltip",
  "validator",
]);
// the suffixes daisyUI owns on the prefixes Tailwind also uses; everything else there is Tailwind's
const DAISY_LIST = new Set(["row", "col-grow", "col-wrap"]);
const DAISY_TAB = new Set(["active", "disabled", "content"]);
const DAISY_SELECT = new Set(["bordered", "ghost", "xs", "sm", "md", "lg", "xl"]);
const DAISY_TABLE = new Set(["zebra", "pin-rows", "pin-cols", "xs", "sm", "md", "lg", "xl"]);
// colour roles, each optionally -content, that a daisyUI colour utility can end in
const DAISY_ROLES = new Set([
  "primary",
  "primary-content",
  "secondary",
  "secondary-content",
  "accent",
  "accent-content",
  "neutral",
  "neutral-content",
  "info",
  "info-content",
  "success",
  "success-content",
  "warning",
  "warning-content",
  "error",
  "error-content",
  "base-100",
  "base-200",
  "base-300",
  "base-content",
]);
// the utilities whose <utility>-<role> form belongs to daisyUI
const DAISY_UTILS = new Set([
  "bg",
  "text",
  "border",
  "ring",
  "fill",
  "stroke",
  "outline",
  "divide",
  "from",
  "via",
  "to",
  "shadow",
  "accent",
  "caret",
  "decoration",
  "placeholder",
]);

const HEX = /#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})(?!\w)/g;
const COLOUR_FN = /\b(?:rgba?|hsla?)\(\s*[\d.]/g;
const PX = /(?<![\w.])\d+(?:\.\d+)?px/g;
const CONTROL = /<(button|input|select|textarea)(?=[\s/>])/g;
const CSS_CLASS = /\.([a-zA-Z][\w-]*)/g;

/** One string literal or template literal: its checked parts (a template's literal text between
 *  the `${…}` gaps) and where its opening delimiter sits. */
type Literal = { parts: { text: string; index: number }[]; open: number };

type Found = Offence & { col: number };

/** The class names legacy.css's selectors define; comments come out first. */
export function legacyWords(css: string): Set<string> {
  const words = new Set<string>();
  const body = css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/\S/g, " "));
  for (const m of body.matchAll(CSS_CLASS)) words.add(m[1] as string);
  return words;
}

/** A token with its Tailwind variants (`hover:`, `sm:dark:`), leading `!` and `/10` opacity off. */
function bareToken(token: string): string {
  let t = token.startsWith("!") ? token.slice(1) : token;
  const slash = t.indexOf("/");
  if (slash >= 0) t = t.slice(0, slash);
  for (;;) {
    const i = t.indexOf(":");
    if (i < 0 || t.slice(0, i).includes("[")) break;
    t = t.slice(i + 1);
  }
  return t;
}

/** Whether a bare token is a daisyUI component, modifier or colour-utility class. */
function isDaisy(token: string): boolean {
  if (DAISY_WORDS.has(token)) return true;
  if (token === "glass" || token === "rounded-box" || token === "rounded-field") return true;
  if (token === "rounded-selector") return true;
  if (token.startsWith("list-")) return DAISY_LIST.has(token.slice(5));
  if (token.startsWith("tab-")) return DAISY_TAB.has(token.slice(4));
  if (token.startsWith("select-")) return DAISY_SELECT.has(token.slice(7)) || DAISY_ROLES.has(token.slice(7));
  if (token.startsWith("table-")) return DAISY_TABLE.has(token.slice(6));
  if (token.startsWith("collapse-")) return token.length > "collapse-".length;
  const dash = token.indexOf("-");
  if (dash < 0) return false;
  const head = token.slice(0, dash);
  if (DAISY_WORDS.has(head)) return true;
  return DAISY_UTILS.has(head) && DAISY_ROLES.has(token.slice(dash + 1));
}

/** The token rules: one token can break several rules at once. */
function tokenOffences(token: string, legacy: Set<string>): Rule[] {
  const rules: Rule[] = [];
  if (isDaisy(token)) rules.push("daisy");
  if (token.startsWith("hr-") && token.length > "hr-".length) rules.push("hr");
  if (legacy.has(token)) rules.push("legacy");
  return rules;
}

/** A line/column reader: one pass over the text, then a binary search per index. */
function lineAt(text: string): (index: number) => { line: number; col: number } {
  const starts = [0];
  for (let i = 0; i < text.length; i++) if (text[i] === "\n") starts.push(i + 1);
  return (index) => {
    let lo = 0;
    let hi = starts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if ((starts[mid] as number) <= index) lo = mid;
      else hi = mid - 1;
    }
    return { line: lo + 1, col: index - (starts[lo] as number) + 1 };
  };
}

function push(found: Found[], rule: Rule, at: { line: number; col: number }, text: string): void {
  found.push({ rule, line: at.line, col: at.col, text });
}

/** Every colour literal in one text: hex, or rgb()/hsl() whose first argument is a number. */
function colourOffences(
  text: string,
  base: number,
  at: (i: number) => { line: number; col: number },
  found: Found[],
): void {
  for (const m of text.matchAll(HEX)) push(found, "colour", at(base + (m.index as number)), m[0]);
  for (const m of text.matchAll(COLOUR_FN)) {
    const start = m.index as number;
    const close = text.indexOf(")", start);
    push(found, "colour", at(base + start), close < 0 ? m[0] : text.slice(start, close + 1));
  }
}

/** Every px length in one text; zero is allowed, and one number is one offence. */
function pxOffences(
  text: string,
  base: number,
  at: (i: number) => { line: number; col: number },
  found: Found[],
): void {
  for (const m of text.matchAll(PX)) {
    if (Number(m[0].slice(0, -2)) !== 0) push(found, "px", at(base + (m.index as number)), m[0]);
  }
}

/** The literal's words, whitespace-split, across all of a template's parts. */
function literalTokens(lit: Literal): string[] {
  return lit.parts
    .map((p) => p.text)
    .join(" ")
    .split(/\s+/)
    .filter((t) => t.length > 0);
}

/** Whether a literal carries classes: it follows class=/className=, is an upper-case const's value
 *  that reads as a class list, or holds several tokens of which one looks like a class (`-`, `:` or
 *  `[`). An upper-case const holds prose, not classes, when a token starts with a capital letter or
 *  the text closes a sentence (`.`, `?` or `!`); a class list has neither. */
function isClassLiteral(text: string, lit: Literal): boolean {
  const before = text.slice(0, lit.open);
  if (/class(?:Name)?=?\{?\s*$/.test(before)) return true;
  const tokens = literalTokens(lit);
  if (/const\s+[A-Z][A-Z0-9_]*\s*=\s*$/.test(before)) {
    return !tokens.some((t) => /^[A-Z]/.test(t)) && !/[.?!]$/.test(tokens.join(" "));
  }
  return tokens.length >= 2 && tokens.some((t) => /[-:[\]]/.test(t));
}

// one frame per open template; code frames born of `${` count their braces to find their `}`
type Frame = { tpl: boolean; braces: number; partStart: number };

/** The lexer's step inside a template frame: the part in progress closes at `` ` `` (template's
 *  end) or `${` (code begins), and `\` carries the next character through. Returns the next index. */
function stepTpl(text: string, i: number, f: Frame, stack: Frame[], literals: Literal[]): number {
  const c = text[i] as string;
  if (c === "\\") return i + 2;
  const last = literals[literals.length - 1];
  if (c === "`") {
    last?.parts.push({ text: text.slice(f.partStart, i), index: f.partStart });
    stack.pop();
    return i + 1;
  }
  if (c === "$" && text[i + 1] === "{") {
    last?.parts.push({ text: text.slice(f.partStart, i), index: f.partStart });
    stack.push({ tpl: false, braces: 0, partStart: 0 });
    return i + 2;
  }
  return i + 1;
}

// characters that leave an expression open, so a `/` after one starts a regex literal, not division
const REGEX_BEFORE = new Set([
  "(",
  ",",
  "=",
  ":",
  "[",
  "!",
  "&",
  "|",
  "?",
  "{",
  ";",
  "+",
  "-",
  "*",
  "%",
  "~",
  "^",
]);

/** Whether the `/` at `i` opens a regex literal: the character before it, ignoring whitespace, is
 *  one that leaves an expression open (or there is none), or the word before it is `return`. `<`,
 *  `>` and `}` are left out on purpose: they precede JSX closing tags and JSX text, which stay code. */
function opensRegex(text: string, i: number): boolean {
  let j = i - 1;
  while (j >= 0 && /\s/.test(text[j] as string)) j -= 1;
  if (j < 0) return true;
  const c = text[j] as string;
  if (REGEX_BEFORE.has(c)) return true;
  if (!/[A-Za-z0-9_$]/.test(c)) return false;
  let k = j;
  while (k >= 0 && /[A-Za-z0-9_$]/.test(text[k] as string)) k -= 1;
  return text.slice(k + 1, j + 1) === "return";
}

/** The index just past the regex literal opening at `i` (its `/`): the closing `/` — the first one
 *  not escaped by `\` and not inside a `[…]` class — plus any flag letters. A regex never spans a
 *  line, so a `/` with no closing one before the newline is division, and only itself is skipped. */
function skipRegex(text: string, i: number): number {
  let j = i + 1;
  let cls = false;
  while (j < text.length) {
    const c = text[j] as string;
    if (c === "\n") return i + 1;
    if (c === "\\") {
      j += 2;
      continue;
    }
    if (cls) {
      if (c === "]") cls = false;
    } else if (c === "[") {
      cls = true;
    } else if (c === "/") {
      j += 1;
      while (j < text.length && /[a-z]/.test(text[j] as string)) j += 1;
      return j;
    }
    j += 1;
  }
  return i + 1;
}

/** Whether the `'` at `i` sits inside a word (`session's`): a letter on both sides. TypeScript
 *  never puts a letter right before a string's opening quote, so this apostrophe opens no string. */
function wordApostrophe(text: string, i: number): boolean {
  return /[A-Za-z]/.test(text[i - 1] as string) && /[A-Za-z]/.test(text[i + 1] as string);
}

/** The index just past the string literal opening at `i` (its quote), the literal recorded. */
function scanString(text: string, i: number, literals: Literal[]): number {
  const quote = text[i] as string;
  let j = i + 1;
  while (j < text.length && text[j] !== quote) {
    if (text[j] === "\\") j += 1;
    j += 1;
  }
  literals.push({ parts: [{ text: text.slice(i + 1, j), index: i + 1 }], open: i });
  return j + 1;
}

/** The index just past the comment opening at `i`, or -1 when no comment opens there: a `//` runs
 *  to the newline, a block comment to its closing delimiter, and the range is recorded either way. */
function skipComment(text: string, i: number, comments: [number, number][]): number {
  if (text[i] !== "/") return -1;
  if (text[i + 1] === "/") {
    const end = text.indexOf("\n", i);
    comments.push([i, end < 0 ? text.length : end]);
    return end < 0 ? text.length : end;
  }
  if (text[i + 1] === "*") {
    const end = text.indexOf("*/", i + 2);
    comments.push([i, end < 0 ? text.length : end + 2]);
    return end < 0 ? text.length : end + 2;
  }
  return -1;
}

/** The lexer's step inside code: the comment ranges, the string literals, a template's opening,
 *  the regex literal, the word-internal apostrophe (a `'` between two letters is prose, never a
 *  string opening), and the braces by which a `${…}`'s closing `}` is told from any other one.
 *  Returns the next index. */
function stepCode(
  text: string,
  i: number,
  f: Frame,
  stack: Frame[],
  literals: Literal[],
  comments: [number, number][],
): number {
  const c = text[i] as string;
  const comment = skipComment(text, i, comments);
  if (comment >= 0) return comment;
  if (c === "/" && opensRegex(text, i)) return skipRegex(text, i);
  if (c === "'" && wordApostrophe(text, i)) return i + 1;
  if (c === "'" || c === '"') return scanString(text, i, literals);
  if (c === "`") {
    literals.push({ parts: [], open: i });
    stack.push({ tpl: true, braces: 0, partStart: i + 1 });
    return i + 1;
  }
  if (c === "{" && stack.length > 1) {
    f.braces += 1;
    return i + 1;
  }
  if (c === "}" && stack.length > 1) {
    if (f.braces === 0) {
      stack.pop();
      const outer = stack[stack.length - 1] as Frame;
      outer.partStart = i + 1;
    } else {
      f.braces -= 1;
    }
    return i + 1;
  }
  return i + 1;
}

/** A tiny lexer for ts/tsx: every string literal and template literal (its parts kept apart),
 *  plus the comment ranges. `${…}` counts as code, so its strings are literals of their own. */
function lex(text: string): { literals: Literal[]; comments: [number, number][] } {
  const literals: Literal[] = [];
  const comments: [number, number][] = [];
  const stack: Frame[] = [{ tpl: false, braces: 0, partStart: 0 }];
  let i = 0;
  while (i < text.length) {
    const f = stack[stack.length - 1] as Frame;
    i = f.tpl ? stepTpl(text, i, f, stack, literals) : stepCode(text, i, f, stack, literals, comments);
  }
  return { literals, comments };
}

/** The same text with its comments' characters blanked, so offsets and lines hold. */
function blankComments(text: string, comments: [number, number][]): string {
  let out = text;
  for (const [a, b] of [...comments].reverse()) {
    out = out.slice(0, a) + out.slice(a, b).replace(/\S/g, " ") + out.slice(b);
  }
  return out;
}

function scanTs(text: string, tsx: boolean, legacy: Set<string>, found: Found[]): void {
  const at = lineAt(text);
  const { literals, comments } = lex(text);
  for (const lit of literals) {
    for (const part of lit.parts) {
      colourOffences(part.text, part.index, at, found);
      pxOffences(part.text, part.index, at, found);
    }
    if (!isClassLiteral(text, lit)) continue;
    for (const part of lit.parts) {
      for (const m of part.text.matchAll(/\S+/g)) {
        const index = part.index + (m.index as number);
        const bare = bareToken(m[0] as string);
        for (const rule of tokenOffences(bare, legacy)) push(found, rule, at(index), bare);
      }
    }
  }
  if (!tsx) return;
  for (const m of blankComments(text, comments).matchAll(CONTROL)) {
    push(found, "control", at(m.index as number), m[0]);
  }
}

function scanCss(text: string, legacy: Set<string>, found: Found[]): void {
  const at = lineAt(text);
  colourOffences(text, 0, at, found);
  pxOffences(text, 0, at, found);
  const selectors = text.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/\S/g, " "));
  for (const m of selectors.matchAll(CSS_CLASS)) {
    const index = m.index as number;
    for (const rule of tokenOffences(m[1] as string, legacy))
      push(found, rule, at(index + 1), m[1] as string);
  }
}

/** One file's offences under the six rules, in reading order. The path only picks the grammar:
 *  `.css` reads selectors as tokens and its whole text for colours and px; `.ts`/`.tsx` read the
 *  string and template literals, and `.tsx` alone reads native control elements. */
export function scanFile(path: string, text: string, legacy: Set<string>): Offence[] {
  const found: Found[] = [];
  if (path.endsWith(".css")) scanCss(text, legacy, found);
  else scanTs(text, path.endsWith(".tsx"), legacy, found);
  return found
    .sort((a, b) => a.line - b.line || a.col - b.col)
    .map((f) => ({ rule: f.rule, line: f.line, text: f.text }));
}

/** The ratchet's answers, in path order: a count may sit at or under its baseline, never over it,
 *  and a baseline entry without its offences asks to be lowered. */
export function ratchet(baseline: Counts, current: Counts): string[] {
  const problems: string[] = [];
  const paths = [...new Set([...Object.keys(baseline), ...Object.keys(current)])].sort();
  for (const path of paths) {
    const base = baseline[path] ?? {};
    const now = current[path] ?? {};
    const rules = [...new Set<Rule>([...Object.keys(base), ...Object.keys(now)] as Rule[])].sort();
    for (const rule of rules) {
      const b = base[rule] ?? 0;
      const n = now[rule] ?? 0;
      if (n > b) problems.push(`${path}: ${rule} is ${n}, over its baseline of ${b}`);
      if (n < b)
        problems.push(`${path}: ${rule} is ${n}, under its baseline of ${b}: lower the baseline to ${n}`);
    }
  }
  return problems;
}
