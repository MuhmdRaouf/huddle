package phone

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// pemType is the Accept token that asks for PEM; anything else takes DER, the form the
// x-x509-ca-cert type and Android's installer expect.
const pemType = "application/x-pem-file"

// Fingerprint answers the root's SHA-256 as paired hex, the shape the terminal prints and the
// guide shows, so the owner compares the computer with the page the phone opened — the check
// that stands in for the signature plain HTTP cannot carry.
func Fingerprint(root *x509.Certificate) string {
	sum := sha256.Sum256(root.Raw)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(pairs, ":")
}

// Identifier answers the profile's stable name: the fingerprint's first eight hex digits.
func Identifier(root *x509.Certificate) string {
	sum := sha256.Sum256(root.Raw)
	return hex.EncodeToString(sum[:4])
}

// MobileConfig answers the unsigned profile that installs the root on iOS: one
// com.apple.security.root payload holding the DER. Unsigned on purpose — a signature would need
// a certificate the phone already trusts, which is the very thing being created; the owner
// compares fingerprints instead, and iOS says "Unsigned" plainly.
func MobileConfig(root *x509.Certificate) []byte {
	id := "dev.huddle.ca." + Identifier(root)
	der := base64.StdEncoding.EncodeToString(root.Raw)
	name := "Huddle Local Authority"
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n" +
		`<plist version="1.0"><dict>` +
		`<key>PayloadContent</key><array><dict>` +
		`<key>PayloadCertificate</key><data>` + der + `</data>` +
		`<key>PayloadDisplayName</key><string>` + name + `</string>` +
		`<key>PayloadIdentifier</key><string>` + id + `</string>` +
		`<key>PayloadType</key><string>com.apple.security.root</string>` +
		`<key>PayloadUUID</key><string>` + id + `</string>` +
		`<key>PayloadVersion</key><integer>1</integer>` +
		`</dict></array>` +
		`<key>PayloadDisplayName</key><string>` + name + `</string>` +
		`<key>PayloadIdentifier</key><string>` + id + `</string>` +
		`<key>PayloadRemovalDisallowed</key><false/>` +
		`<key>PayloadType</key><string>Configuration</string>` +
		`<key>PayloadUUID</key><string>` + id + `</string>` +
		`<key>PayloadVersion</key><integer>1</integer>` +
		`</dict></plist>`)
}

// WriteCA answers one root-download request: the iOS profile for /ca.mobileconfig, DER for
// /ca.crt (PEM when the Accept names it). Only the public certificate, never a key.
func WriteCA(w http.ResponseWriter, r *http.Request, root *x509.Certificate, mobile bool) {
	if mobile {
		w.Header().Set("Content-Type", "application/x-apple-aspen-config")
		_, _ = w.Write(MobileConfig(root))
		return
	}
	if acceptsPEM(r.Header.Get("Accept")) {
		w.Header().Set("Content-Type", pemType)
		_, _ = w.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	_, _ = w.Write(root.Raw)
}

// acceptsPEM says whether an Accept header names the PEM type.
func acceptsPEM(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		if strings.TrimSpace(strings.SplitN(part, ";", 2)[0]) == pemType {
			return true
		}
	}
	return false
}

// PageOptions shape the guide page.
type PageOptions struct {
	// Root is the CA whose fingerprint the page shows.
	Root *x509.Certificate
	// HubURL is the hub's https base the Check proves trust against; "" hides the check.
	HubURL string
}

// Page answers the guide the phone opens: the fingerprint to compare with the computer's
// terminal, the download and the exact taps for iOS and Android (the phone's own user agent
// picks one), and the check that says Trusted once the hub's https answers.
func Page(o PageOptions) []byte {
	hub := html.EscapeString(o.HubURL)
	check := ""
	if hub != "" {
		check = `<section class="card">
<h2>Then check</h2>
<p>Open the hub from this phone and see it trusted:</p>
<p><button id="check" data-hub="` + hub + `" type="button">Check</button> <span id="check-out" aria-live="polite"></span></p>
</section>
`
	}
	return []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Trust Huddle on this phone</title>
<style>
:root{--bg:#e6e9ef;--panel:#eff1f5;--border:#ccd0da;--text:#4c4f69;--muted:#5c5f77;--blue:#1e66f5;--green:#1d8a4e;color-scheme:light}
@media (prefers-color-scheme:dark){:root{--bg:#181825;--panel:#1e1e2e;--border:#313244;--text:#cdd6f4;--muted:#a6adc8;--blue:#89b4fa;--green:#a6e3a1;color-scheme:dark}}
*{box-sizing:border-box}body{margin:0;padding:16px;background:var(--bg);color:var(--text);font:15px/1.5 Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;-webkit-font-smoothing:antialiased}
main{max-width:32rem;margin:0 auto;display:flex;flex-direction:column;gap:14px}
h1{font-size:24px;margin:8px 0 0}h2{font-size:16px;margin:0 0 8px}
.sub{color:var(--muted);margin:4px 0 8px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:16px}
ol{margin:0;padding-left:20px;display:flex;flex-direction:column;gap:8px}
.fp{font-family:ui-monospace,"SF Mono",Menlo,monospace;font-size:13px;word-break:break-all;display:block;margin-top:6px}
a{color:var(--blue)}button{font:inherit;border:1px solid var(--border);background:var(--panel);color:var(--text);border-radius:8px;padding:8px 14px}
#check-out.ok{color:var(--green);font-weight:600}#check-out.bad{color:var(--muted)}
footer{color:var(--muted);font-size:12px;text-align:center;margin-top:4px}
body.ios section.android,body.android section.ios{display:none}
</style>
</head>
<body>
<main>
<h1>Trust Huddle on this phone</h1>
<p class="sub">This page comes from the computer running huddle. It hands the phone the root its hub signs with, so the hub opens without a certificate warning.</p>
<section class="card">
<h2>Check the fingerprint</h2>
<p>Compare with the line the computer printed beside its QR code:</p>
<code class="fp">` + Fingerprint(o.Root) + `</code>
</section>
<section class="card ios">
<h2>iPhone or iPad</h2>
<ol>
<li><a href="/ca.mobileconfig" download>Download the profile</a>, then open Settings.</li>
<li>Settings &rarr; Profile Downloaded &rarr; Install.</li>
<li>Settings &rarr; General &rarr; About &rarr; Certificate Trust Settings &rarr; turn on <b>Huddle Local Authority</b>.</li>
</ol>
</section>
<section class="card android">
<h2>Android</h2>
<ol>
<li><a href="/ca.crt" download>Download the certificate</a>.</li>
<li>Settings &rarr; Security &rarr; Encryption &amp; credentials &rarr; Install a certificate &rarr; CA certificate.</li>
</ol>
</section>
` + check + `<footer>This page stops itself after 10 minutes.</footer>
</main>
<script>
(function () {
  var ua = navigator.userAgent;
  var ios = /iPhone|iPad|iPod/.test(ua), android = /Android/.test(ua);
  if (ios && !android) { document.body.className = "ios"; }
  else if (android && !ios) { document.body.className = "android"; }
  var button = document.getElementById("check");
  if (!button) { return; }
  var out = document.getElementById("check-out");
  button.addEventListener("click", function () {
    out.textContent = "checking…";
    out.className = "";
    // no-cors: the answer's body stays opaque, and the fetch itself succeeds only once the
    // phone trusts the root the page installs
    fetch(button.dataset.hub + "/health?ts=" + Date.now(), { mode: "no-cors", cache: "no-store" })
      .then(function () { out.textContent = "Trusted"; out.className = "ok"; })
      .catch(function () { out.textContent = "Not trusted yet — finish the steps above, then check again"; out.className = "bad"; });
  });
})();
</script>
</body>
</html>`)
}

// securityHeaders carries the guide's own answers: the policy admits this one page's inline
// style and script (it has no assets to load and must open before any trust exists) and, when a
// hub stands behind it, that hub's origin for the check alone.
func securityHeaders(w http.ResponseWriter, hubURL string) {
	connect := "'self'"
	if hubURL != "" {
		connect = "'self' " + hubURL
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; connect-src "+connect+"; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
