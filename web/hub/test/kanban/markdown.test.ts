// markdown.test.ts — the body renderer: the subset it turns into HTML, and everything else
// escaped in place.
import { describe, expect, it } from "vite-plus/test";
import { renderMarkdown } from "../../src/kanban/markdown.ts";

describe("renderMarkdown", () => {
  it("renders headings at their levels, a top-level one demoted to h2", () => {
    expect(renderMarkdown("## Two\n### Three\n#### Four\n# One")).toBe(
      "<h2>Two</h2>\n<h3>Three</h3>\n<h4>Four</h4>\n<h2>One</h2>",
    );
  });

  it("renders ordered and unordered lists, one item per line", () => {
    expect(renderMarkdown("1. one\n2. two\n\n- a\n- b")).toBe(
      "<ol>\n<li>one</li>\n<li>two</li>\n</ol>\n<ul>\n<li>a</li>\n<li>b</li>\n</ul>",
    );
  });

  it("joins the lines of a paragraph and closes it at a blank line", () => {
    expect(renderMarkdown("first\nsecond\n\nthird")).toBe("<p>first second</p>\n<p>third</p>");
  });

  it("keeps a fenced code block verbatim, escaped, even unterminated", () => {
    expect(renderMarkdown('```go\nfmt.Println("a < b")\n```')).toBe(
      "<pre><code>fmt.Println(&quot;a &lt; b&quot;)</code></pre>",
    );
    expect(renderMarkdown("```js\nlet x = 1;\n")).toBe("<pre><code>let x = 1;\n</code></pre>");
  });

  it("renders the inline constructs: code, bold, emphasis and links", () => {
    expect(renderMarkdown("`code` and **bold** and *em* and [text](https://example.com/x)")).toBe(
      '<p><code>code</code> and <strong>bold</strong> and <em>em</em> and <a href="https://example.com/x" rel="noopener noreferrer" target="_blank">text</a></p>',
    );
  });

  it("keeps only the destinations a browser may take from us", () => {
    expect(renderMarkdown("[ok](mailto:a@b.c) [root](/x) [here](./y) [tag](#z)")).toBe(
      '<p><a href="mailto:a@b.c" rel="noopener noreferrer" target="_blank">ok</a> <a href="/x" rel="noopener noreferrer" target="_blank">root</a> <a href="./y" rel="noopener noreferrer" target="_blank">here</a> <a href="#z" rel="noopener noreferrer" target="_blank">tag</a></p>',
    );
    expect(renderMarkdown("[dead](javascript:alert(1))")).toContain('href="#"');
  });

  it("escapes raw HTML and the HTML-significant characters everywhere", () => {
    expect(renderMarkdown("<script>alert('x')</script>\n\n<b>bold of distrust</b> & \"quotes\"")).toBe(
      "<p>&lt;script&gt;alert('x')&lt;/script&gt;</p>\n<p>&lt;b&gt;bold of distrust&lt;/b&gt; &amp; &quot;quotes&quot;</p>",
    );
  });

  it("renders the empty body as nothing", () => {
    expect(renderMarkdown("")).toBe("");
  });
});
