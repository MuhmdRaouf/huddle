package server

// The machine's three roles (SPEC 3) as the running server proves them: solo and satellite keep
// one listener on loopback — a satellite's leftover hub.listen is refused with one line — and
// hub adds the TLS listener on hub.listen, where only a paired device's cookie or a satellite's
// credential gets past the gate; the one-time link that signs a loopback browser in is refused
// there, and the passkey ceremonies stay open.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// roleHome answers a temp home whose config.toml carries the body given, laid out the way a
// server start finds it.
func roleHome(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// selfSignedCert writes the pair a hub listener serves TLS from: tls/cert.pem and tls/key.pem,
// one certificate for the hub's name and the loopback alias the test reaches it on.
func selfSignedCert(t *testing.T, tlsDir, name string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		IPAddresses:           []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "cert.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "key.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hubAddress reads the hub listener's address from the server's own line, which is how an
// operator (and this suite) learns where the second listener answers.
func hubAddress(t *testing.T, srv *childServer) string {
	t.Helper()
	if !until(10*time.Second, func() bool {
		return regexp.MustCompile(`hub listener on https://`).MatchString(srv.log.joined())
	}) {
		t.Fatalf("no hub listener line: %s", srv.log.joined())
	}
	match := regexp.MustCompile(`hub listener on https://(\S+)`).FindStringSubmatch(srv.log.joined())
	if match == nil {
		t.Fatalf("the hub listener line carries no address: %s", srv.log.joined())
	}
	return match[1]
}

// hubDo runs one request against the hub listener: TLS with the test's self-signed certificate,
// the Host header the address itself spells.
func hubDo(t *testing.T, srv *childServer, method, path, body string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	base := "https://" + hubAddress(t, srv)
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, base+path, payload)
	if err != nil {
		t.Fatalf("build the %s %s request: %v", method, path, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // the test's own certificate
		DisableKeepAlives: true,
	}, Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the %s %s request failed: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s %s answer: %v", method, path, err)
	}
	return response.StatusCode, response.Header, string(raw)
}

// hubPairPhone pairs one device through the hub listener from the origin a phone on the private
// network speaks, and pins what that path owes: the relying party is the hub's own name, and the
// finish sets the __Host- cookie TLS allows.
func hubPairPhone(t *testing.T, srv *childServer, origin string) {
	t.Helper()
	invite := pairCore(t, srv)
	minted, err := invite.Mint(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	headers := map[string]string{"content-type": "application/json", ActionHeader: "/api/pair/begin"}
	code, _, out := hubDo(t, srv, http.MethodPost, "/api/pair/begin",
		fmt.Sprintf(`{"token":%q,"name":%q}`, minted.Token, "Pixel"), headers)
	if code != 200 {
		t.Fatalf("pair begin over the hub listener = %d %s", code, out)
	}
	var body struct {
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("pair begin body %s: %v", out, err)
	}
	var begin struct {
		PublicKey struct {
			RP struct {
				ID string `json:"id"`
			} `json:"rp"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(body.Options, &begin); err != nil {
		t.Fatalf("pair begin options %s: %v", body.Options, err)
	}
	if begin.PublicKey.RP.ID != "hub.mine.test" {
		t.Fatalf("the relying party = %q, want the hub's own name", begin.PublicKey.RP.ID)
	}
	finish, err := json.Marshal(map[string]any{
		"token": minted.Token, "response": webauthntest.NewSoftKey(t).Create(t, body.Options, origin),
	})
	if err != nil {
		t.Fatalf("encode the finish body: %v", err)
	}
	headers[ActionHeader] = "/api/pair/finish"
	code, header, out := hubDo(t, srv, http.MethodPost, "/api/pair/finish", string(finish), headers)
	if code != 200 {
		t.Fatalf("pair finish over the hub listener = %d %s", code, out)
	}
	setCookie := header.Get("Set-Cookie")
	if !strings.HasPrefix(setCookie, auth.PairHostCookie+"=hdev_") || !strings.Contains(setCookie, "Secure") {
		t.Fatalf("the hub listener's Set-Cookie %q is not the __Host- cookie TLS allows", setCookie)
	}
}

// loopbackCode opens one sign-in link on the loopback listener without following its redirect,
// answering the 303's own headers: the pair a spent code is proven by.
func loopbackCode(t *testing.T, srv *childServer, code string) (int, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, srv.base+"/?code="+code, nil)
	if err != nil {
		t.Fatalf("build the link request: %v", err)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open the link: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, response.Header
}

// satelliteCredential draws one satellite credential and records its digest in the child's own
// hub database, the way the hub's minting word will.
func satelliteCredential(t *testing.T, srv *childServer, machine string) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("draw: %v", err)
	}
	credential := "hsat_" + base64.RawURLEncoding.EncodeToString(raw)
	db, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the child's hub.db: %v", err)
	}
	defer db.Close()
	if err := db.SaveSatellite(context.Background(), auth.PairDigest(credential), machine,
		time.Now().UnixMilli()); err != nil {
		t.Fatalf("save the satellite: %v", err)
	}
	return credential
}

// TestSoloKeepsOneLoopbackListener pins the default role: one listener on 127.0.0.1, and a
// leftover hub.listen from another role opens nothing.
func TestSoloKeepsOneLoopbackListener(t *testing.T) {
	dir := roleHome(t, "role = \"solo\"\n\n[hub]\nlisten = \"127.0.0.2:0\"\n")
	srv := startServer(t, map[string]string{"HUDDLE_HOME": dir})
	if strings.Contains(srv.log.joined(), "hub listener on") {
		t.Fatalf("solo opened a hub listener: %s", srv.log.joined())
	}
	if code, _, _ := srv.get(t, "/health", nil); code != 200 {
		t.Fatalf("loopback /health = %d, want 200", code)
	}
}

// TestSatelliteNeverListensBeyondLoopback pins the satellite role: its own sessions are served
// exactly as solo's, and a hub.listen still set is ignored with one line in the log.
func TestSatelliteNeverListensBeyondLoopback(t *testing.T) {
	dir := roleHome(t, "role = \"satellite\"\n\n[hub]\nurl = \"https://hub.mine.test:8443\"\nlisten = \"127.0.0.2:0\"\n")
	srv := startServer(t, map[string]string{"HUDDLE_HOME": dir})
	if strings.Contains(srv.log.joined(), "hub listener on") {
		t.Fatalf("a satellite opened a hub listener: %s", srv.log.joined())
	}
	if !strings.Contains(srv.log.joined(), "role satellite: hub.listen 127.0.0.2:0 is ignored") {
		t.Fatalf("no line names the ignored hub.listen: %s", srv.log.joined())
	}
	if code, _, _ := srv.get(t, "/health", nil); code != 200 {
		t.Fatalf("loopback /health = %d, want 200", code)
	}
}

// TestHubListenerServesPairedDevicesAndSatellites is the hub role whole: the TLS listener answers
// on hub.listen, refuses a request with no credential, carries a paired device's cookie and a
// satellite's bearer, refuses the one-time link that signs a loopback browser in, and keeps the
// passkey ceremonies open — while the loopback listener answers as it always did. The listen
// address is the IPv6 loopback alias: a second address this machine owns, the way the NetBird
// address is in production, that binds with no alias set up first.
func TestHubListenerServesPairedDevicesAndSatellites(t *testing.T) {
	listenPort := freePort(t)
	dir := roleHome(t, fmt.Sprintf("role = \"hub\"\n\n[hub]\nlisten = \"[::1]:%d\"\nhost = \"hub.mine.test\"\n", listenPort))
	selfSignedCert(t, filepath.Join(dir, "tls"), "hub.mine.test")
	srv := startServer(t, map[string]string{"HUDDLE_HOME": dir})

	// no credential and a credential nobody holds: both refused at the gate
	if code, _, out := hubDo(t, srv, http.MethodGet, "/api/channels", "", nil); code != 401 {
		t.Fatalf("an empty request = %d %s, want the gate's 401", code, out)
	}
	if code, _, out := hubDo(t, srv, http.MethodGet, "/api/channels", "",
		map[string]string{"Authorization": "Bearer hsat_nobodyholdsthiscredential0000000000"}); code != 401 {
		t.Fatalf("an unknown bearer = %d %s, want the gate's 401", code, out)
	}

	// a satellite's credential passes the gate and reads as that machine
	credential := satelliteCredential(t, srv, "mbp")
	if code, _, out := hubDo(t, srv, http.MethodGet, "/api/channels", "",
		map[string]string{"Authorization": "Bearer " + credential}); code != 200 {
		t.Fatalf("the satellite's read = %d %s, want 200", code, out)
	}

	// a paired device's cookie, earned on the loopback listener, carries it here too
	key := webauthntest.NewSoftKey(t)
	setCookie, _ := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	if code, _, out := hubDo(t, srv, http.MethodGet, "/api/channels", "",
		map[string]string{"Cookie": auth.PairHostCookie + "=" + cookie}); code != 200 {
		t.Fatalf("the paired device's read = %d %s, want 200", code, out)
	}

	// the one-time link never signs a browser in from the network, however valid it is: a fresh
	// code earned on the loopback listener (303, a cookie) is refused outright on the hub one
	fresh := func() string {
		code, out := srv.post(t, "/api/login", "{}", nil)
		if code != 200 {
			t.Fatalf("mint a login code = %d %s", code, out)
		}
		var answer struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(out), &answer); err != nil || answer.Code == "" {
			t.Fatalf("the login answer %s carries no code", out)
		}
		return answer.Code
	}
	spent := fresh()
	if status, header := loopbackCode(t, srv, spent); status != 303 || header.Get("Set-Cookie") == "" {
		t.Fatalf("the code spent on loopback = %d with cookie %q, want 303 and one", status, header.Get("Set-Cookie"))
	}
	code, header, out := hubDo(t, srv, http.MethodGet, "/?code="+fresh(), "", nil)
	if code != 401 {
		t.Fatalf("a fresh code on the hub listener = %d %s, want 401", code, out)
	}
	if header.Get("Set-Cookie") != "" {
		t.Fatalf("the refused link set a cookie: %q", header.Get("Set-Cookie"))
	}
	if !strings.Contains(out, "passkey") {
		t.Fatalf("the refusal %q does not name the passkey", out)
	}

	// the passkey ceremonies stay open: a browser that holds nothing can still begin a sign-in
	if code, _, out := hubDo(t, srv, http.MethodPost, "/api/login/begin", "{}",
		map[string]string{"content-type": "application/json", ActionHeader: "/api/login/begin"}); code != 200 {
		t.Fatalf("the sign-in ceremony = %d %s, want 200", code, out)
	}

	// a phone pairs over the hub listener itself, from the origin the private network gives it:
	// the relying party is the hub's name, and the session cookie takes the __Host- spelling TLS
	// allows
	hubPairPhone(t, srv, fmt.Sprintf("https://hub.mine.test:%d", listenPort))

	// the loopback listener answers as it always did
	if code, _, _ := srv.get(t, "/health", nil); code != 200 {
		t.Fatalf("loopback /health = %d, want 200", code)
	}
}

// TestHubListenerWhoseCertificateIsACMEsToObtainStaysOnLoopback pins the graceful half: a hub
// role whose certificate an ACME setup promises but tls/ does not hold yet warns once and keeps
// serving loopback, never stranding the machine — the local CA never stands in for a
// certificate `huddle cert` still has to obtain.
func TestHubListenerWhoseCertificateIsACMEsToObtainStaysOnLoopback(t *testing.T) {
	dir := roleHome(t, "role = \"hub\"\n\n[hub]\nlisten = \"[::1]:0\"\nhost = \"hub.mine.test\"\n\n[cert]\ndns_provider = \"fakeprov\"\n")
	srv := startServer(t, map[string]string{"HUDDLE_HOME": dir})
	if strings.Contains(srv.log.joined(), "hub listener on") {
		t.Fatalf("a hub whose certificate is ACME's to obtain opened a listener: %s", srv.log.joined())
	}
	if !strings.Contains(srv.log.joined(), "certificate") {
		t.Fatalf("no line names the missing certificate: %s", srv.log.joined())
	}
	if _, err := os.Stat(filepath.Join(dir, "pki")); err == nil {
		t.Fatalf("the local CA stood in for an ACME setup: %s", srv.log.joined())
	}
	if code, _, _ := srv.get(t, "/health", nil); code != 200 {
		t.Fatalf("loopback /health = %d, want 200", code)
	}
}
