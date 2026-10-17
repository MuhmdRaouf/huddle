/**
 * Ticket bodies are untrusted text: raw HTML is never rendered, only escaped in place, and links
 * open in their own tab with no window handle handed over. The subset a board writes: headings,
 * lists, fenced code and paragraphs, with code spans, bold, emphasis and links inline.
 */

function escapeHtml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

/** Only destinations the browser can take from us; anything else stays a dead anchor. */
function safeHref(href: string): string {
  const trimmed = href.trim();
  return /^(https?:|mailto:|\/|\.\/|#)/i.test(trimmed) ? escapeHtml(trimmed) : "#";
}

/** The inline constructs, leftmost wins: code span, bold, emphasis, then the labelled link. The
 *  link's destination runs to the first closing parenthesis; safeHref vetoes what remains. */
const INLINE = /`([^`]+)`|\*\*([^*]+)\*\*|\*([^*\s][^*]*)\*|\[([^\]]+)\]\(([^)\s]*)\)/g;

function inline(source: string): string {
  let out = "";
  let at = 0;
  for (const match of source.matchAll(INLINE)) {
    const [whole, code, bold, em, label, href] = match;
    out += escapeHtml(source.slice(at, match.index ?? 0));
    if (code !== undefined) out += `<code>${escapeHtml(code)}</code>`;
    else if (bold !== undefined) out += `<strong>${escapeHtml(bold)}</strong>`;
    else if (em !== undefined) out += `<em>${escapeHtml(em)}</em>`;
    else
      out += `<a href="${safeHref(href ?? "")}" rel="noopener noreferrer" target="_blank">${escapeHtml(label ?? "")}</a>`;
    at = (match.index ?? 0) + whole.length;
  }
  return out + escapeHtml(source.slice(at));
}

/** One line-based pass: a list stays open while its items come, a blank line closes everything. */
export function renderMarkdown(source: string): string {
  const out: string[] = [];
  let list: "ul" | "ol" | null = null;
  let fence: string[] | null = null;
  let para: string[] = [];
  const closeList = (): void => {
    if (list !== null) out.push(`</${list}>`);
    list = null;
  };
  const flushPara = (): void => {
    if (para.length > 0) out.push(`<p>${inline(para.join(" "))}</p>`);
    para = [];
  };
  for (const raw of source.split("\n")) {
    const line = raw.replace(/\s+$/, "");
    if (fence !== null) {
      if (line.startsWith("```")) {
        out.push(`<pre><code>${escapeHtml(fence.join("\n"))}</code></pre>`);
        fence = null;
      } else fence.push(raw);
      continue;
    }
    if (line.startsWith("```")) {
      flushPara();
      closeList();
      fence = [];
      continue;
    }
    const heading = /^(#{1,4})\s+(.*)$/.exec(line);
    if (heading !== null) {
      flushPara();
      closeList();
      const level = (heading[1] ?? "#").length;
      const text = inline(heading[2] ?? "");
      out.push(level <= 2 ? `<h2>${text}</h2>` : level === 3 ? `<h3>${text}</h3>` : `<h4>${text}</h4>`);
      continue;
    }
    const ul = /^\s*[-*]\s+(.*)$/.exec(line);
    if (ul !== null) {
      flushPara();
      if (list !== "ul") {
        closeList();
        out.push("<ul>");
        list = "ul";
      }
      out.push(`<li>${inline(ul[1] ?? "")}</li>`);
      continue;
    }
    const ol = /^\s*\d+\.\s+(.*)$/.exec(line);
    if (ol !== null) {
      flushPara();
      if (list !== "ol") {
        closeList();
        out.push("<ol>");
        list = "ol";
      }
      out.push(`<li>${inline(ol[1] ?? "")}</li>`);
      continue;
    }
    if (line.trim() === "") {
      flushPara();
      closeList();
      continue;
    }
    para.push(line.trim());
  }
  if (fence !== null) out.push(`<pre><code>${escapeHtml(fence.join("\n"))}</code></pre>`);
  flushPara();
  closeList();
  return out.join("\n");
}
