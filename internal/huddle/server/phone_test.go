package server

// The phone guide over the real server: the plain listener serves the local CA's public root
// (DER by default, PEM by Accept, and the iOS profile as a plist holding that DER), the owner's
// "Add a phone" starts one helper at a time with its QR served beside it, and the guide ends
// with the server. Every helper binds the hub listener's own loopback address; no test touches
// a trust store.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hubPhoneServer spawns a hub-role server whose listener binds any loopback port, so the phone
// helper binds loopback too.
func hubPhoneServer(t *testing.T) *childServer {
	t.Helper()
	home := t.TempDir()
	config := "role = \"hub\"\n[hub]\nlisten = \"127.0.0.1:0\"\nhost = \"hub.mine.test\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return startServer(t, map[string]string{"HUDDLE_HOME": home, "HUDDLE_DATA": t.TempDir()})
}

// homeRootDER answers the DER of the root the home's pki/ holds.
func homeRootDER(t *testing.T, srv *childServer) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(srv.home, "pki", "root.crt"))
	if err != nil {
		t.Fatalf("read the home's root: %v", err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		t.Fatal("pki/root.crt holds no certificate")
	}
	return block.Bytes
}

func TestThePlainListenerServesTheLocalCARoot(t *testing.T) {
	srv := startServer(t, nil)

	// DER by default, and the request itself lays the CA
	code, header, body := srv.get(t, "/ca.crt", nil)
	if code != 200 || header.Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("/ca.crt = (%d, %q), want 200 and the x509 type", code, header.Get("Content-Type"))
	}
	if body != string(homeRootDER(t, srv)) {
		t.Error("/ca.crt did not serve exactly the bytes pki/root.crt holds")
	}

	// PEM when the Accept names it, byte for byte the file's own spelling
	code, _, pemBody := srv.get(t, "/ca.crt", map[string]string{"Accept": "application/x-pem-file"})
	if code != 200 {
		t.Fatalf("/ca.crt as PEM = %d, want 200", code)
	}
	fileBody, err := os.ReadFile(filepath.Join(srv.home, "pki", "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if pemBody != string(fileBody) {
		t.Error("the PEM answer differs from pki/root.crt's own bytes")
	}

	// the iOS profile parses as a plist holding that DER
	code, _, profile := srv.get(t, "/ca.mobileconfig", nil)
	if code != 200 {
		t.Fatalf("/ca.mobileconfig = %d, want 200", code)
	}
	var doc struct {
		XMLName xml.Name `xml:"plist"`
		Data    []string `xml:"dict>array>dict>data"`
	}
	if err := xml.Unmarshal([]byte(profile), &doc); err != nil {
		t.Fatalf("the profile does not parse as an XML plist: %v", err)
	}
	if len(doc.Data) == 0 {
		t.Fatalf("the profile holds no certificate data:\n%s", profile)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(doc.Data[0]))
	if err != nil {
		t.Fatalf("the profile's certificate data does not decode: %v", err)
	}
	if string(raw) != string(homeRootDER(t, srv)) {
		t.Error("the profile holds a root other than pki/root.crt's")
	}
}

func TestABrokenCARefusesTheCARoute(t *testing.T) {
	srv := startServer(t, nil)
	if code, _, _ := srv.get(t, "/ca.crt", nil); code != 200 {
		t.Fatal("the first read did not lay and serve the root")
	}
	if err := os.WriteFile(filepath.Join(srv.home, "pki", "root.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, body := srv.get(t, "/ca.crt", nil)
	if code < 500 || !strings.Contains(body, "cannot serve") {
		t.Fatalf("/ca.crt over a broken CA = (%d, %s), want a 500 naming the CA", code, body)
	}
}

func TestAddAPhoneServesTheQRAndOneGuideAtATime(t *testing.T) {
	srv := hubPhoneServer(t)
	root := map[string]string{"x-huddle-token": srv.token}

	code, out := srv.post(t, "/api/phone", `{"action":"start"}`, root)
	if code != 200 {
		t.Fatalf("start the guide = %d: %s", code, out)
	}
	var started struct {
		URL         string `json:"url"`
		Fingerprint string `json:"fingerprint"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatalf("the start's answer does not parse: %v: %s", err, out)
	}
	if !strings.HasPrefix(started.URL, "http://127.0.0.1:") || started.ExpiresIn != 600 {
		t.Fatalf("the guide's answer is %+v, want a loopback URL and 600 seconds", started)
	}
	// the fingerprint is the root the plain listener serves
	sum := sha256.Sum256(homeRootDER(t, srv))
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	if started.Fingerprint != strings.Join(pairs, ":") {
		t.Errorf("the guide's fingerprint is %s, want the served root's", started.Fingerprint)
	}

	// the guide answers on its own port, hub and fingerprint aboard
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(started.URL)
	if err != nil {
		t.Fatalf("open the guide: %v", err)
	}
	page, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{started.Fingerprint, "https://hub.mine.test", "ca.mobileconfig"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the guide does not carry %q", want)
		}
	}

	// a second start answers the same guide, not a second one
	code, out = srv.post(t, "/api/phone", `{"action":"start"}`, root)
	if code != 200 {
		t.Fatalf("the second start = %d: %s", code, out)
	}
	var again struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &again); err != nil || again.URL != started.URL {
		t.Fatalf("the second start answered %s, want the standing guide %s", out, started.URL)
	}

	// the QR is served beside it
	code, header, svg := srv.get(t, "/api/phone/qr.svg", root)
	if code != 200 || header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("/api/phone/qr.svg = (%d, %q), want 200 and the svg type", code, header.Get("Content-Type"))
	}
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "</svg>") {
		t.Errorf("the QR is not an SVG document:\n%s", svg)
	}
}

func TestThePhoneRoutesNameTheirShapes(t *testing.T) {
	srv := hubPhoneServer(t)
	root := map[string]string{"x-huddle-token": srv.token}

	// the QR names what to do when no guide stands
	if code, _, body := srv.get(t, "/api/phone/qr.svg", root); code != 404 || !strings.Contains(body, "start one first") {
		t.Fatalf("the QR with no guide = (%d, %s), want 404 naming the start", code, body)
	}
	// a wrong action is refused
	if code, body := srv.post(t, "/api/phone", `{"action":"nope"}`, root); code != 400 || !strings.Contains(body, "action") {
		t.Fatalf("a wrong action = (%d, %s), want 400 naming the action", code, body)
	}
	// a caller without the owner's credential never starts one
	if code, _ := srv.post(t, "/api/phone", `{"action":"start"}`, map[string]string{
		"x-huddle-token": "not-a-token", "content-type": "application/json"}); code != 401 {
		t.Fatalf("a stranger's start = %d, want 401", code)
	}
}

func TestTheGuideEndsWithTheServer(t *testing.T) {
	srv := hubPhoneServer(t)
	root := map[string]string{"x-huddle-token": srv.token}
	code, out := srv.post(t, "/api/phone", `{"action":"start"}`, root)
	if code != 200 {
		t.Fatalf("start the guide = %d: %s", code, out)
	}
	var started struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	srv.kill()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(started.URL)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("the guide answered after the server ended, want the connection refused")
	}
}
