/**
 * Ticket bodies are untrusted text: raw HTML is never rendered, only escaped in place, and links open
 * in their own tab with no window handle handed over.
 */

import { Marked, type Tokens } from "marked";

function escapeHtml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

/** Only destinations the browser can take from us; anything else stays a dead anchor. */
function safeHref(href: string): string {
  const trimmed = href.trim();
  return /^(https?:|mailto:|\/|\.\/|#)/i.test(trimmed) ? escapeHtml(trimmed) : "#";
}

const md = new Marked();

md.use({
  renderer: {
    html(token: Tokens.HTML | Tokens.Tag) {
      return escapeHtml(token.text);
    },
    link(token: Tokens.Link) {
      const href = safeHref(token.href);
      const title = token.title == null || token.title === "" ? "" : ` title="${escapeHtml(token.title)}"`;
      const label =
        token.tokens !== undefined && token.tokens.length > 0
          ? this.parser.parseInline(token.tokens)
          : escapeHtml(token.text);
      return `<a href="${href}"${title} rel="noopener noreferrer" target="_blank">${label}</a>`;
    },
  },
});

export function renderMarkdown(source: string): string {
  return md.parse(source, { async: false });
}
