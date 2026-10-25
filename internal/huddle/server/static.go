package server

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// immutable is the Cache-Control of a file named by its content hash: the browser keeps it a year
// and never asks again, because new bytes always come under a new name.
const immutable = "public, max-age=31536000, immutable"

// shortCache is the Cache-Control of the PWA files the manifest names under fixed names: an hour,
// so a phone reopening the app does not fetch them on every launch.
const shortCache = "public, max-age=3600"

// hashedName admits the shell's hashed scripts and stylesheet the build writes at dist's root
// (app.<hash>.js, app.<hash>.css, theme-boot.<hash>.js): the regex admits no "/" and no "..".
var hashedName = regexp.MustCompile(`^[a-z-]+\.[A-Za-z0-9_-]{8}\.(?:js|css)$`)

// precompressed lists the copies the build writes beside each hashed asset, best first, by the
// Content-Encoding each answers with.
var precompressed = []struct{ encoding, ext string }{{"br", ".br"}, {"gzip", ".gz"}}

// hashedRoute serves one hashed asset as immutable, in the best precompressed copy the request's
// Accept-Encoding admits, or as built when it admits none.
func (s *server) hashedRoute(w http.ResponseWriter, r *http.Request, name string) error {
	contentType := staticTypes[name[strings.LastIndex(name, "."):]]
	body, err := dashboardFile(name)
	if err != nil {
		writeText(w, 404, "not found", nil)
		return nil
	}
	headers := map[string]string{"Content-Type": contentType, "Cache-Control": immutable, "Vary": "Accept-Encoding"}
	for _, variant := range precompressed {
		if !acceptsEncoding(r.Header.Get("Accept-Encoding"), variant.encoding) {
			continue
		}
		if encoded, err := dashboardFile(name + variant.ext); err == nil {
			body = encoded
			headers["Content-Encoding"] = variant.encoding
			break
		}
	}
	writeText(w, 200, string(body), headers)
	return nil
}

// acceptsEncoding answers whether an Accept-Encoding header admits one coding: named, or "*", with
// a q-value above zero (RFC 9110 12.5.3).
func acceptsEncoding(header, coding string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != coding && name != "*" {
			continue
		}
		q := 1.0
		if value, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				q = parsed
			}
		}
		return q > 0
	}
	return false
}
