package server

import (
	"regexp"
	"strings"
	"testing"
)

// The PWA's own routes: the manifest answers with its type, the service worker with the root
// scope it registers for and no caching, the icons as PNGs — and nothing else under /icons/.
func TestPWARoutes(t *testing.T) {
	srv := startServer(t, nil)

	t.Run("(a) /manifest.webmanifest answers application/manifest+json", func(t *testing.T) {
		code, header, body := srv.get(t, "/manifest.webmanifest", nil)
		if code != 200 || header.Get("Content-Type") != "application/manifest+json" {
			t.Fatalf("/manifest.webmanifest = %d %q", code, header.Get("Content-Type"))
		}
		m := bodyMap(t, body)
		if m["name"] != "Huddle" || m["display"] != "standalone" || m["start_url"] != "/#/now" {
			t.Fatalf("the manifest holds %v / %v / %v", m["name"], m["display"], m["start_url"])
		}
	})

	t.Run("(b) /sw.js is a worker script allowed the root scope, uncached", func(t *testing.T) {
		code, header, body := srv.get(t, "/sw.js", nil)
		if code != 200 || !strings.Contains(header.Get("Content-Type"), "text/javascript") {
			t.Fatalf("/sw.js = %d %q", code, header.Get("Content-Type"))
		}
		if header.Get("Service-Worker-Allowed") != "/" {
			t.Fatalf("Service-Worker-Allowed = %q, want /", header.Get("Service-Worker-Allowed"))
		}
		if header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", header.Get("Cache-Control"))
		}
		if !strings.Contains(body, `addEventListener("fetch"`) {
			t.Fatal("the worker catches no fetch")
		}
	})

	t.Run("(c) the icons answer image/png with PNG bytes; anything else under /icons/ is a 404", func(t *testing.T) {
		for _, name := range []string{"icon-192.png", "icon-512.png", "icon-maskable-512.png", "icon-badge-96.png"} {
			code, header, body := srv.get(t, "/icons/"+name, nil)
			if code != 200 || !strings.Contains(header.Get("Content-Type"), "image/png") {
				t.Fatalf("/icons/%s = %d %q", name, code, header.Get("Content-Type"))
			}
			if !strings.HasPrefix(body, "\x89PNG") {
				t.Fatalf("/icons/%s is not a PNG", name)
			}
		}
		for _, path := range []string{"/icons/nope.png", "/icons/icon-192.gif", "/icons/../sw.js"} {
			if code, _, _ := srv.get(t, path, nil); code != 404 {
				t.Fatalf("%s = %d, want 404", path, code)
			}
		}
	})
}

// The static shell the server hands out: every asset the page loads is served, nothing points
// off-box or at a route the build no longer ships, and the compiled CSS carries the theme —
// daisyUI's dark default and the light side the scheme switch keys.
func TestUI(t *testing.T) {
	srv := startServer(t, nil)

	css, js := shellAsset(t, srv, "app", ".css"), shellAsset(t, srv, "app", ".js")

	t.Run("(a) / is HTML that loads the hashed stylesheet and app module", func(t *testing.T) {
		code, header, body := srv.get(t, "/", nil)
		if code != 200 {
			t.Fatalf("/ = %d, want 200", code)
		}
		if !strings.Contains(header.Get("Content-Type"), "text/html") {
			t.Fatalf("content-type = %q", header.Get("Content-Type"))
		}
		for _, want := range []string{css, js, `<div id="app"></div>`} {
			if !strings.Contains(body, want) {
				t.Fatalf("/ does not contain %q", want)
			}
		}
	})

	t.Run("(b) the stylesheet and the app module answer 200 with the right type", func(t *testing.T) {
		cssCode, cssHeader, _ := srv.get(t, css, nil)
		if cssCode != 200 || !strings.Contains(cssHeader.Get("Content-Type"), "text/css") {
			t.Fatalf("%s = %d %q", css, cssCode, cssHeader.Get("Content-Type"))
		}
		jsCode, jsHeader, _ := srv.get(t, js, nil)
		if jsCode != 200 || !strings.Contains(jsHeader.Get("Content-Type"), "text/javascript") {
			t.Fatalf("%s = %d %q", js, jsCode, jsHeader.Get("Content-Type"))
		}
	})

	t.Run("(c) the served HTML and JS point nowhere off-box: no http(s) URL beyond the w3.org namespace", func(t *testing.T) {
		_, _, html := srv.get(t, "/", nil)
		_, _, script := srv.get(t, js, nil)
		urlRe := regexp.MustCompile(`https?://[^\s"'` + "`" + `)<]+`)
		for _, u := range urlRe.FindAllString(html+script, -1) {
			if !strings.HasPrefix(u, "http://www.w3.org/") {
				t.Fatalf("an off-box URL in the shell: %s", u)
			}
		}
	})

	t.Run("(d) the compiled CSS is Catppuccin through the shared theme, with daisyUI's components", func(t *testing.T) {
		_, _, body := srv.get(t, css, nil)
		css := strings.ToLower(body)
		for _, want := range []string{"#1e1e2e", "#cba6f7", "#89b4fa", "#11111b", "#eff1f5", "#8839ef", "#1e66f5", "#4c4f69"} {
			if !strings.Contains(css, want) {
				t.Fatalf("the CSS misses the theme colour %s", want)
			}
		}
		for _, gone := range []string{"#0b0f14", "#22d3ee", "#34d399"} {
			if strings.Contains(css, gone) {
				t.Fatalf("the CSS carries the foreign colour %s", gone)
			}
		}
		if !strings.Contains(css, "prefers-color-scheme:dark") || !strings.Contains(css, ":root[data-scheme=light]") {
			t.Fatal("the CSS misses the dark default or the light side")
		}
		for _, part := range []string{".btn{", ".badge{", ".menu{", ".card{", ".join{", ".stat{", ".skeleton{", ".kbd{"} {
			if !strings.Contains(css, part) {
				t.Fatalf("the CSS misses daisyUI's %s", part)
			}
		}
		if regexp.MustCompile(`url\((["']?)https?://`).MatchString(css) {
			t.Fatal("the CSS loads something from the network")
		}
		if strings.Contains(css, "url(/fonts/") {
			t.Fatal("the CSS points at /fonts/, which the build no longer ships")
		}
	})

	t.Run("(e) no route serves fonts: /fonts/x.woff2 answers 404 as any unknown path does", func(t *testing.T) {
		if code, _, _ := srv.get(t, "/fonts/x.woff2", nil); code != 404 {
			t.Fatalf("/fonts/x.woff2 = %d, want 404", code)
		}
	})
}

// The signed-out page loads its look from /signed-out.css: the CSP admits no inline style, so a
// page that carried one would render bare.
func TestTheSignedOutPageStylesComeFromItsOwnStylesheet(t *testing.T) {
	if strings.Contains(signedOutPage("1.2.3"), "<style") {
		t.Fatal("the signed-out page carries an inline style the CSP blocks")
	}
	if !strings.Contains(signedOutPage("1.2.3"), `<link rel="stylesheet" href="/signed-out.css">`) {
		t.Fatal("the signed-out page links no stylesheet")
	}
	srv := startServer(t, nil)
	code, header, body := srv.get(t, "/signed-out.css", nil)
	if code != 200 || header.Get("Content-Type") != "text/css" {
		t.Fatalf("/signed-out.css = %d %q", code, header.Get("Content-Type"))
	}
	if header.Get("Content-Security-Policy") != ContentSecurityPolicy || !strings.Contains(body, "--bg:") {
		t.Fatalf("/signed-out.css answered %q under %q", body, header.Get("Content-Security-Policy"))
	}
}
