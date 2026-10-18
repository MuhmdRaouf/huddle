package phone

// The helper against a real root minted in a temp directory: it serves exactly its three paths
// for its ten minutes, answers nothing else, and stops on its deadline (a movable clock), on its
// context, and on Close. Every bind is loopback port 0; nothing here touches a trust store.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/pki"
)

// mintRoot answers a fresh local-CA root in one temp directory, the way the hub listener and the
// trust words lay it.
func mintRoot(t *testing.T) *x509.Certificate {
	t.Helper()
	root, err := pki.New(t.TempDir(), pki.Options{}).Root()
	if err != nil {
		t.Fatalf("mint a root: %v", err)
	}
	return root
}

// fakeClock is the movable clock the deadline test advances.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

// get answers one request over url ("" status and a nil body when the connection is refused).
func get(t *testing.T, url string, accept string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return response.StatusCode, response.Header, body
}

func TestTheHelperServesItsThreePathsAndRefusesEverythingElse(t *testing.T) {
	root := mintRoot(t)
	release := make(chan struct{})
	clock := &fakeClock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	// the watchdog parks in its one wait until the test lets it go, so the paths answer while
	// the helper lives
	sleep := func(ctx context.Context, d time.Duration) bool {
		select {
		case <-release:
		case <-ctx.Done():
			return false
		}
		clock.now = clock.now.Add(d)
		return true
	}
	helper, err := Start(context.Background(), HelperOptions{
		Address: "127.0.0.1", Root: root, HubURL: "https://hub.mine.test:8443", Now: clock.Now, Sleep: sleep,
	})
	if err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	defer helper.Close()
	base := helper.URL()
	if !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.HasSuffix(base, "/") {
		t.Fatalf("the helper's URL is %q, want http://127.0.0.1:<port>/", base)
	}

	// the guide page carries the fingerprint, both platforms' steps and the check for the hub
	// behind it, and the policy admits that hub's origin for the check alone
	code, header, page := get(t, base, "")
	if code != 200 || header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("the guide = (%d, %q), want 200 and html", code, header.Get("Content-Type"))
	}
	for _, want := range []string{
		Fingerprint(root), "ca.mobileconfig", "ca.crt", "Huddle Local Authority", `id="check"`,
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the guide does not carry %q", want)
		}
	}
	if csp := header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' https://hub.mine.test:8443") {
		t.Errorf("the guide's policy does not admit the hub's origin: %q", csp)
	}

	// the root as DER by default, exactly the certificate's own bytes
	code, header, der := get(t, base+"ca.crt", "")
	if code != 200 || header.Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("/ca.crt = (%d, %q), want 200 and the x509 type", code, header.Get("Content-Type"))
	}
	if string(der) != string(root.Raw) {
		t.Errorf("/ca.crt served %d bytes, want the root's %d DER bytes", len(der), len(root.Raw))
	}

	// the root as PEM when the client asks for it
	code, _, pemBody := get(t, base+"ca.crt", "application/x-pem-file, application/json;q=0.9")
	if code != 200 {
		t.Fatalf("/ca.crt as PEM = %d, want 200", code)
	}
	block, _ := pem.Decode(pemBody)
	if block == nil || string(block.Bytes) != string(root.Raw) {
		t.Error("the PEM answer does not hold the root's DER")
	}

	// the iOS profile parses as a plist holding the same DER
	code, header, profile := get(t, base+"ca.mobileconfig", "")
	if code != 200 || header.Get("Content-Type") != "application/x-apple-aspen-config" {
		t.Fatalf("/ca.mobileconfig = (%d, %q), want 200 and the profile type", code, header.Get("Content-Type"))
	}
	assertProfileHoldsRoot(t, profile, root)

	// nothing else answers: not a key, not the home's own paths, not a spelled traversal
	for _, path := range []string{
		"root.key", "ca.key", "pki/root.crt", "pki/root.key", "intermediate.crt",
		"index.js", "ca.crt/", "CA.CRT", "%2e%2e/root.key", "ca.crt%00",
	} {
		if code, _, _ = get(t, base+path, ""); code != 404 {
			t.Errorf("/%s answered %d, want 404", path, code)
		}
	}

	close(release)
	select {
	case <-helper.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the helper never ended after its deadline")
	}
	if code, _, _ = get(t, base+"ca.crt", ""); code != 0 {
		t.Errorf("the helper answered %d after its deadline, want the connection refused", code)
	}
}

func TestTheHelperStopsAfterItsDeadlineOnAMovableClock(t *testing.T) {
	root := mintRoot(t)
	clock := &fakeClock{now: time.Now()}
	helper, err := Start(context.Background(), HelperOptions{
		Address: "127.0.0.1", Root: root,
		Now: clock.Now,
		Sleep: func(ctx context.Context, d time.Duration) bool {
			// one wait that moves the clock the whole way at once
			clock.now = clock.now.Add(d)
			return true
		},
	})
	if err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	select {
	case <-helper.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the helper outlived its deadline")
	}
	if helper.Live() {
		t.Error("the helper says it is live after its deadline")
	}
	if code, _, _ := get(t, helper.URL()+"ca.crt", ""); code != 0 {
		t.Errorf("the helper answered %d after its deadline, want the connection refused", code)
	}
}

func TestTheHelperStopsWhenItsContextEndsAndOnClose(t *testing.T) {
	root := mintRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	helper, err := Start(ctx, HelperOptions{Address: "127.0.0.1", Root: root})
	if err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	base := helper.URL()
	if code, _, _ := get(t, base+"ca.crt", ""); code != 200 {
		t.Fatalf("the helper answered %d while it should be live, want 200", code)
	}
	cancel()
	select {
	case <-helper.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the helper never ended after its context ended")
	}

	// a second helper ends on Close alone, well before any deadline
	helper2, err := Start(context.Background(), HelperOptions{Address: "127.0.0.1", Root: root})
	if err != nil {
		t.Fatalf("start the second helper: %v", err)
	}
	if code, _, _ := get(t, helper2.URL(), ""); code != 200 {
		t.Fatalf("the second helper answered %d while live, want 200", code)
	}
	helper2.Close()
	helper2.Close() // a second Close changes nothing
	select {
	case <-helper2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the second helper never ended after Close")
	}
	if code, _, _ := get(t, helper2.URL()+"ca.crt", ""); code != 0 {
		t.Errorf("the second helper answered %d after Close, want the connection refused", code)
	}
}

func TestStartRefusesAnAddressItCannotBind(t *testing.T) {
	root := mintRoot(t)
	// 203.0.113.1 is documentation range: no interface holds it, so the bind cannot land
	if _, err := Start(context.Background(), HelperOptions{Address: "203.0.113.1", Root: root}); err == nil {
		t.Fatal("Start over an address no interface holds answered nil, want the bind refusal")
	}
}

// The page hides the check when no hub stands behind it, and wires the hub's origin into the
// check and the policy when one does.
func TestThePageCarriesTheCheckOnlyForAHub(t *testing.T) {
	root := mintRoot(t)
	bare := Page(PageOptions{Root: root})
	if strings.Contains(string(bare), `id="check"`) {
		t.Error("a page with no hub still offers the check button")
	}
	withHub := Page(PageOptions{Root: root, HubURL: "https://hub.mine.test:8443"})
	for _, want := range []string{`id="check"`, "https://hub.mine.test:8443"} {
		if !strings.Contains(string(withHub), want) {
			t.Errorf("the page with a hub does not carry %q", want)
		}
	}
}

// The fingerprint is the DER's SHA-256 as paired hex, and the identifier the profile carries is
// its first eight digits.
func TestTheFingerprintAndIdentifier(t *testing.T) {
	root := mintRoot(t)
	sum := sha256.Sum256(root.Raw)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	pairs := make([]string, 0, len(sum))
	for i := 0; i < len(hexed); i += 2 {
		pairs = append(pairs, hexed[i:i+2])
	}
	if fp := Fingerprint(root); fp != strings.Join(pairs, ":") {
		t.Errorf("the fingerprint is %s, want %s", fp, strings.Join(pairs, ":"))
	}
	if id := Identifier(root); id != strings.ToLower(hexed[:8]) {
		t.Errorf("the identifier is %s, want %s", id, strings.ToLower(hexed[:8]))
	}
	if !strings.Contains(string(MobileConfig(root)), "dev.huddle.ca."+Identifier(root)) {
		t.Error("the profile does not carry its identifier")
	}
}

// The LAN walk answers an address or nothing, never a loopback one.
func TestTheLANWalkAnswersAnAddressOrNothing(t *testing.T) {
	got := LANAddress()
	if got == "" {
		return // a machine with no outside interface: the callers fall back to loopback
	}
	ip := net.ParseIP(got)
	if ip == nil || ip.IsLoopback() || ip.To4() == nil {
		t.Errorf("LANAddress answered %q, want an IPv4 the phone can reach", got)
	}
}

// BindHost prefers the hub listener's own address, then the walk's answer, then loopback.
func TestBindHostPrefersTheHubListenerThenTheWalk(t *testing.T) {
	for _, c := range []struct {
		listen string
		lan    func() string
		want   string
	}{
		{"192.168.1.5:9443", nil, "192.168.1.5"},
		{"0.0.0.0:9443", func() string { return "192.168.1.9" }, "192.168.1.9"},
		{":9443", func() string { return "192.168.1.9" }, "192.168.1.9"},
		{"", func() string { return "10.0.0.4" }, "10.0.0.4"},
		{"", func() string { return "" }, "127.0.0.1"},
		{"hub.mine.test:9443", func() string { return "192.168.1.9" }, "192.168.1.9"},
	} {
		if got := BindHost(c.listen, c.lan); got != c.want {
			t.Errorf("BindHost(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
}

// HubURL spells the hub's https base: every port but 443 is written out.
func TestHubURLAppendsEveryPortBut443(t *testing.T) {
	for _, c := range []struct {
		host   string
		listen string
		want   string
	}{
		{"hub.mine.test", "0.0.0.0:9443", "https://hub.mine.test:9443"},
		{"hub.mine.test", "0.0.0.0:443", "https://hub.mine.test"},
		{"hub.mine.test", "", "https://hub.mine.test"},
		{"hub.mine.test", "0.0.0.0:0", "https://hub.mine.test"},
	} {
		if got := HubURL(c.host, c.listen); got != c.want {
			t.Errorf("HubURL(%q, %q) = %q, want %q", c.host, c.listen, got, c.want)
		}
	}
}

// QRSVG answers a QR the shape of scannable SVG: an svg element, a white plate, and one path.
func TestQRSVGAnswersScannableSVG(t *testing.T) {
	svg, err := QRSVG("http://127.0.0.1:51111/")
	if err != nil {
		t.Fatalf("QRSVG: %v", err)
	}
	text := string(svg)
	for _, want := range []string{"<svg", "</svg>", "viewBox=", "<path", "#fff", "#000"} {
		if !strings.Contains(text, want) {
			t.Errorf("the QR SVG does not carry %q:\n%s", want, text)
		}
	}
	if _, err := QRSVG(""); err != nil {
		t.Errorf("an empty text still encodes: %v", err)
	}
}

// assertProfileHoldsRoot parses the served profile as the XML plist it claims to be and wants
// the certificate data inside to be exactly the root's DER.
func assertProfileHoldsRoot(t *testing.T, profile []byte, root *x509.Certificate) {
	t.Helper()
	var doc struct {
		XMLName xml.Name `xml:"plist"`
		Data    []string `xml:"dict>array>dict>data"`
	}
	if err := xml.Unmarshal(profile, &doc); err != nil {
		t.Fatalf("the profile does not parse as an XML plist: %v\n%s", err, profile)
	}
	if len(doc.Data) == 0 {
		t.Fatalf("the profile holds no certificate data:\n%s", profile)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(doc.Data[0]))
	if err != nil {
		t.Fatalf("the payload's certificate data does not decode: %v", err)
	}
	if string(raw) != string(root.Raw) {
		t.Errorf("the payload holds %d bytes, want the root's %d DER bytes", len(raw), len(root.Raw))
	}
	if !strings.Contains(string(profile), "com.apple.security.root") {
		t.Error("the payload is not a com.apple.security.root one")
	}
}
