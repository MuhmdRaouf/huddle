package server

import (
	"regexp"
	"testing"
)

// shellAsset reads the shell page and answers the hashed path it loads for one asset, named by its
// stem and extension: shellAsset(t, srv, "app", ".js") is /static/app.<hash>.js.
func shellAsset(t *testing.T, srv *childServer, stem, ext string) string {
	t.Helper()
	_, _, page := srv.get(t, "/", nil)
	path := regexp.MustCompile(`/static/` + stem + `\.[A-Za-z0-9_-]{8}\` + ext + `"`).FindString(page)
	if path == "" {
		t.Fatalf("index.html loads no hashed /static/%s.<hash>%s", stem, ext)
	}
	return path[:len(path)-1]
}

// A hashed asset never changes under its name, so the browser keeps it a year without asking
// again; the bytes go out precompressed in the best encoding the browser offers, and the shell
// page that names them is never stored, so a new build is seen on the next open.
func TestHashedAssetsAreImmutableAndPrecompressed(t *testing.T) {
	srv := startServer(t, nil)
	const wantCache = "public, max-age=31536000, immutable"

	for _, path := range []string{
		shellAsset(t, srv, "app", ".css"), shellAsset(t, srv, "theme-boot", ".js"), shellAsset(t, srv, "app", ".js"),
	} {
		for _, tc := range []struct{ accept, encoding string }{
			{"gzip, deflate, br", "br"},
			{"gzip", "gzip"},
			{"identity", ""},
		} {
			code, header, body := srv.get(t, path, map[string]string{"Accept-Encoding": tc.accept})
			if code != 200 {
				t.Fatalf("%s with %q = %d", path, tc.accept, code)
			}
			if got := header.Get("Content-Encoding"); got != tc.encoding {
				t.Fatalf("%s with %q: Content-Encoding %q, want %q", path, tc.accept, got, tc.encoding)
			}
			if got := header.Get("Cache-Control"); got != wantCache {
				t.Fatalf("%s: Cache-Control %q, want %q", path, got, wantCache)
			}
			if got := header.Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("%s: Vary %q, want Accept-Encoding", path, got)
			}
			if tc.encoding != "" && len(body) == 0 {
				t.Fatalf("%s with %q answers no bytes", path, tc.accept)
			}
		}
	}

	// the fonts carry their hash in the name too; the PWA files under fixed names keep an hour
	_, _, sheet := srv.get(t, shellAsset(t, srv, "app", ".css"), nil)
	font := regexp.MustCompile(`/fonts/[^)]+\.woff2`).FindString(sheet)
	for path, want := range map[string]string{
		"/":                     "no-store",
		"/index.html":           "no-store",
		font:                    wantCache,
		"/icons/icon-192.png":   "public, max-age=3600",
		"/manifest.webmanifest": "public, max-age=3600",
	} {
		code, header, _ := srv.get(t, path, nil)
		if code != 200 || header.Get("Cache-Control") != want {
			t.Fatalf("%s = %d, Cache-Control %q, want 200 %q", path, code, header.Get("Cache-Control"), want)
		}
	}
}
