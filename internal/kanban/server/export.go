package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/watch"
	"github.com/MuhmdRaouf/huddle/internal/kanban/web"
)

// The export: the board as one page that answers every request from itself.
// The UI the server serves is inlined — its script and stylesheet embedded in
// the HTML — and the data the page boots from sits in a script ahead of the app
// script, so the exported board renders offline, with no server and no network.

// exportPayload is the page's data script: the snapshot the live page loads
// and every ticket body, keyed by id.
type exportPayload struct {
	Snapshot watch.Snapshot        `json:"snapshot"`
	Bodies   map[string]ticketBody `json:"bodies"`
}

// ExportPage answers the board as one self-contained page and its ticket
// count. The board is read from its files through one watcher pass; no server
// needs to run.
func ExportPage(ctx context.Context, b board.Board, now func() time.Time) ([]byte, int, error) {
	return exportPage(ctx, b, now, web.FS())
}

// exportPage builds the page over one file set.
func exportPage(ctx context.Context, b board.Board, now func() time.Time, files fs.FS) ([]byte, int, error) {
	w := watch.New(b, watch.Options{Now: now})
	_, _ = w.Scan(ctx) // the first pass fills the snapshot; an empty board still builds one
	snap := w.Snapshot()

	bodies := make(map[string]ticketBody, len(snap.Cards))
	for _, card := range snap.Cards {
		if _, seen := bodies[card.ID]; seen {
			continue // two files with one id show twice and open one body
		}
		shown, err := b.Show(ctx, card.ID)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", card.ID, err)
		}
		fields := shown.Fields
		if fields == nil {
			fields = map[string]string{}
		}
		bodies[card.ID] = ticketBody{Card: shown.Card, Fields: fields, Body: shown.Body, Path: shown.Path}
	}

	// The default HTML escaping is the point here: the payload sits inside a
	// script element, and every <, > and & comes out as <, > and
	// &, so no ticket text — a body holding </script> or <!-- — can end
	// the script early or open an escape, while JSON.parse still reads the
	// original text back.
	data, err := json.Marshal(exportPayload{Snapshot: snap, Bodies: bodies})
	if err != nil {
		return nil, 0, err
	}
	page, err := inlineBoardPage(files, data)
	if err != nil {
		return nil, 0, err
	}
	return page, len(snap.Cards), nil
}

// scriptSrcRe matches one external script tag; linkHrefRe one stylesheet
// link. The build emits one of each.
var (
	scriptSrcRe = regexp.MustCompile(`(?s)<script\b[^>]*\bsrc="([^"]+)"[^>]*>\s*</script>`)
	linkHrefRe  = regexp.MustCompile(`<link\b[^>]*\bhref="([^"]+)"[^>]*>`)
	endScriptRe = regexp.MustCompile(`(?i)</script`)
	endStyleRe  = regexp.MustCompile(`(?i)</style`)
)

// inlineBoardPage answers the served index.html with every referenced asset
// embedded: stylesheets become style elements, scripts become inline scripts
// with the data script set just before the app script. An asset the file set
// does not hold is an error — a page that would reach the network is never
// written.
func inlineBoardPage(files fs.FS, data []byte) ([]byte, error) {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read the board page: %w", err)
	}

	assets := map[string][]byte{}
	for _, re := range []*regexp.Regexp{linkHrefRe, scriptSrcRe} {
		for _, match := range re.FindAllSubmatch(page, -1) {
			name := strings.TrimPrefix(string(match[1]), "./")
			content, err := fs.ReadFile(files, name)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			assets[name] = content
		}
	}

	withStyles := linkHrefRe.ReplaceAllFunc(page, func(tag []byte) []byte {
		css := assets[strings.TrimPrefix(string(linkHrefRe.FindSubmatch(tag)[1]), "./")]
		inner := append([]byte("<style>\n"), guardInline(css, endStyleRe, `<\/style`)...)
		return append(inner, "\n</style>"...)
	})

	return scriptSrcRe.ReplaceAllFunc(withStyles, func(tag []byte) []byte {
		js := assets[strings.TrimPrefix(string(scriptSrcRe.FindSubmatch(tag)[1]), "./")]
		out := []byte("<script>\nwindow.__KANBAN__ = ")
		out = append(out, data...)
		out = append(out, ";</script>\n<script type=\"module\">\n"...)
		out = append(out, guardInline(js, endScriptRe, `<\/script`)...)
		return append(out, "\n</script>"...)
	}), nil
}

// guardInline answers content that cannot end its element early: the HTML
// parser closes a script or a style at its end tag wherever those bytes sit,
// so each one is split with a backslash — `\/` reads as `/` in a JavaScript
// string, template or regex and in a CSS string, leaving the content intact.
func guardInline(content []byte, endTag *regexp.Regexp, escaped string) []byte {
	return endTag.ReplaceAll(content, []byte(escaped))
}
