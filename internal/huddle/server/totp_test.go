package server

// The authenticator routes over the real server: the enrolment a link session or a signed-in
// session opens (a bare visit can never enrol), the step-up replacing an enrolment asks for,
// the code sign-in that needs no credential at all, the lockout's word, and the passkey a code
// session can save on its own device row. The codes come from a hand-rolled RFC 6238, so the
// routes are proven against the specification, not against the library.

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// rfc6238 answers the code the specification draws for one secret at one step.
func rfc6238(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatalf("decode the secret: %v", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	at := int(sum[len(sum)-1] & 0x0f)
	code := (uint32(sum[at])&0x7f)<<24 | uint32(sum[at+1])<<16 | uint32(sum[at+2])<<8 | uint32(sum[at+3])
	return fmt.Sprintf("%06d", code%1_000_000)
}

// nowStep is the RFC 6238 counter the wall clock names now.
func nowStep() int64 { return time.Now().Unix() / 30 }

// totpBegin opens one enrolment and answers its secret.
func totpBegin(t *testing.T, srv *childServer, headers map[string]string) string {
	t.Helper()
	code, out := srv.post(t, "/api/totp/begin", `{}`, headers)
	if code != 200 {
		t.Fatalf("totp begin = %d: %s", code, out)
	}
	var begun struct {
		URI    string   `json:"uri"`
		Secret string   `json:"secret"`
		QR     []string `json:"qr"`
	}
	if err := json.Unmarshal([]byte(out), &begun); err != nil {
		t.Fatalf("totp begin body %s: %v", out, err)
	}
	if !strings.HasPrefix(begun.URI, "otpauth://totp/") || !strings.Contains(begun.URI, "secret="+begun.Secret) {
		t.Fatalf("the begun enrolment %s does not carry its secret", begun.URI)
	}
	if len(begun.QR) == 0 {
		t.Fatalf("the begun enrolment answers no QR rows")
	}
	return begun.Secret
}

// totpFinish confirms one begun enrolment over HTTP and answers the recovery codes.
func totpFinish(t *testing.T, srv *childServer, headers map[string]string, secret string) []string {
	t.Helper()
	code, out := srv.post(t, "/api/totp/finish", fmt.Sprintf(`{"code":%q}`, rfc6238(t, secret, nowStep())), headers)
	if code != 200 {
		t.Fatalf("totp finish = %d: %s", code, out)
	}
	var finished struct {
		Recovery []string `json:"recovery"`
	}
	if err := json.Unmarshal([]byte(out), &finished); err != nil || len(finished.Recovery) != 10 {
		t.Fatalf("the enrolment's answer %s carries no ten recovery codes (%v)", out, err)
	}
	return finished.Recovery
}

// enrolTOTP runs the whole enrolment over HTTP and answers the secret it enrolled with its
// recovery codes.
func enrolTOTP(t *testing.T, srv *childServer, headers map[string]string) (string, []string) {
	t.Helper()
	secret := totpBegin(t, srv, headers)
	return secret, totpFinish(t, srv, headers, secret)
}

// codeSignIn posts one code to the uncredentialed sign-in and answers status, body and cookie.
func codeSignIn(t *testing.T, srv *childServer, code string) (int, string, string) {
	t.Helper()
	status, header, out, err := srv.request(context.Background(), http.MethodPost, "/api/login/totp",
		fmt.Sprintf(`{"code":%q}`, code), map[string]string{
			"content-type": "application/json",
			// the browser the code signs in: its user agent is the name its device row takes
			"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
		})
	if err != nil {
		t.Fatalf("code sign-in: %v", err)
	}
	cookie := ""
	if set := header.Get("Set-Cookie"); set != "" {
		if !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "SameSite=Strict") {
			t.Fatalf("the code sign-in's cookie misses its own promises: %q", set)
		}
		cookie = strings.SplitN(set, ";", 2)[0]
	}
	return status, out, cookie
}

// TestTOTPEnrolNeedsASession proves a bare visit can never enrol: no credential answers 401, a
// member 403, and the owner's link session — the proof of machine access — enrols with no
// step-up at all.
func TestTOTPEnrolNeedsASession(t *testing.T) {
	srv := startServer(t, nil)

	code, out := srv.post(t, "/api/totp/begin", `{}`, map[string]string{"x-huddle-token": ""})
	if code != 401 {
		t.Errorf("an enrolment with no credential = %d (%s), want 401", code, out)
	}

	_, body := srv.post(t, "/api/tokens", "{}", nil)
	invite, _ := bodyMap(t, body)["token"].(string)
	_, body = srv.post(t, "/api/join", `{"token": "`+invite+`", "name": "dev"}`, nil)
	cred, _ := bodyMap(t, body)["credential"].(string)
	code, out = srv.post(t, "/api/totp/begin", `{}`, map[string]string{"x-huddle-token": cred})
	if code != 403 {
		t.Errorf("a member's enrolment = %d (%s), want 403", code, out)
	}

	totpBegin(t, srv, linkBrowser(t, srv))
}

// TestTOTPFinishStoresNothingOnAWrongCode proves the enrolment's second half: a wrong code
// answers 400 and leaves the home un-enrolled, and the right one enrols and answers ten
// recovery codes exactly once.
func TestTOTPFinishStoresNothingOnAWrongCode(t *testing.T) {
	srv := startServer(t, nil)
	owner := linkBrowser(t, srv)

	totpBegin(t, srv, owner)
	code, out := srv.post(t, "/api/totp/finish", `{"code":"000000"}`, owner)
	if code != 400 {
		t.Errorf("a wrong finish code = %d (%s), want 400", code, out)
	}
	if _, _, available := srv.get(t, "/api/login/available", nil); !strings.Contains(available, `"totp":false`) {
		t.Errorf("a wrong code left the home enrolled: %s", available)
	}

	_, recovery := enrolTOTP(t, srv, owner)
	if len(recovery) != 10 {
		t.Fatalf("the enrolment answered %d recovery codes, want 10", len(recovery))
	}
	if _, _, available := srv.get(t, "/api/login/available", nil); !strings.Contains(available, `"totp":true`) {
		t.Errorf("the enrolment did not land: %s", available)
	}
}

// TestTOTPReplaceNeedsAStepUp proves the owner's decision on replacing: the first enrolment on
// a fresh link session needs no assertion, and every later enrolment asks the passkey for one.
func TestTOTPReplaceNeedsAStepUp(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	device := deviceHeaders(cookie)

	_, _ = enrolTOTP(t, srv, linkBrowser(t, srv))

	// the pairing's own assertion has aged out of its window: replacing asks for a fresh one
	stepUpStamp(t, srv, cookie, time.Now().Add(-10*time.Minute).UnixMilli())
	code, out := srv.post(t, "/api/totp/begin", `{}`, device)
	if code != 401 || out != stepUpRequiredBody {
		t.Errorf("a replacing begin without an assertion = %d (%s), want the step-up word", code, out)
	}
	if status, body := stepUpOverHTTP(t, srv, key, cookie, userHandle); status != 200 {
		t.Fatalf("the step-up itself = %d: %s", status, body)
	}
	totpBegin(t, srv, device)
}

// TestTOTPSignInByCode proves the uncredentialed route: a valid code mints a cookie the shell
// reads as a device session named for the browser, the same code never works twice, a wrong
// code is the plain 401, and a recovery code signs in once.
func TestTOTPSignInByCode(t *testing.T) {
	srv := startServer(t, nil)
	owner := linkBrowser(t, srv)
	secret, recovery := enrolTOTP(t, srv, owner)

	one := rfc6238(t, secret, nowStep())
	status, out, cookie := codeSignIn(t, srv, one)
	if status != 200 {
		t.Fatalf("a valid code = %d: %s", status, out)
	}
	if !strings.HasPrefix(cookie, "huddle=hdev_") {
		t.Fatalf("the code sign-in set no session cookie: %q", cookie)
	}

	// the browser it names reads as a device session on the device its user agent spells
	headers := map[string]string{"Cookie": cookie, "x-huddle-token": ""}
	code, _, who := srv.get(t, "/api/whoami", headers)
	if code != 200 || !strings.Contains(who, `"kind":"device"`) || !strings.Contains(who, `"device":"Mac"`) {
		t.Fatalf("the code session's whoami = %d %s, want the device Mac", code, who)
	}
	if !strings.Contains(who, `"totp":true`) || !strings.Contains(who, `"device_passkey":false`) {
		t.Fatalf("the code session's whoami = %s, want the totp facts", who)
	}
	code, _, list := srv.get(t, "/api/devices", headers)
	if code != 200 || !strings.Contains(list, `"name":"Mac"`) {
		t.Fatalf("the devices list = %d %s, want Mac listed", code, list)
	}

	status, out, _ = codeSignIn(t, srv, one)
	if status != 401 {
		t.Errorf("the same code again = %d (%s), want 401", status, out)
	}
	status, out, _ = codeSignIn(t, srv, "123456")
	if status != 401 {
		t.Errorf("a wrong code = %d (%s), want 401", status, out)
	}

	// a recovery code works once, in either spelling
	status, out, _ = codeSignIn(t, srv, strings.ToUpper(recovery[0]))
	if status != 200 {
		t.Fatalf("a recovery code = %d: %s", status, out)
	}
	status, out, _ = codeSignIn(t, srv, recovery[0])
	if status != 401 {
		t.Errorf("the spent recovery code = %d (%s), want 401", status, out)
	}
}

// TestTOTPRemovalEndsTheCodeSessions proves the off switch over HTTP: the removal from a
// signed-in browser ends every session a code minted, and the second removal says there is
// nothing there.
func TestTOTPRemovalEndsTheCodeSessions(t *testing.T) {
	srv := startServer(t, nil)
	owner := linkBrowser(t, srv)
	secret, _ := enrolTOTP(t, srv, owner)

	_, out, cookie := codeSignIn(t, srv, rfc6238(t, secret, nowStep()))
	if out == "" {
		t.Fatal("the code sign-in failed")
	}
	headers := map[string]string{"Cookie": cookie, "x-huddle-token": ""}
	if code, _, who := srv.get(t, "/api/whoami", headers); code != 200 {
		t.Fatalf("the code session's whoami = %d %s", code, who)
	}

	status, _, body, err := srv.request(context.Background(), http.MethodDelete, "/api/totp", "", owner)
	if err != nil || status != 200 {
		t.Fatalf("the removal = %d: %s (%v)", status, body, err)
	}
	if code, _, who := srv.get(t, "/api/whoami", headers); code != 401 {
		t.Errorf("a code session outlived the removal: %d %s", code, who)
	}
	if _, _, available := srv.get(t, "/api/login/available", nil); !strings.Contains(available, `"totp":false`) {
		t.Errorf("the removal left the home enrolled: %s", available)
	}
	status, _, body, err = srv.request(context.Background(), http.MethodDelete, "/api/totp", "", owner)
	if err != nil || status != 404 {
		t.Errorf("the second removal = %d: %s (%v), want 404", status, body, err)
	}
}

// TestSessionPasskeyServesTheCodeSession proves the routes the biometrics offer runs: they need
// a device session (a link browser has no device row to land one on), and the finish leaves
// the row holding the passkey its browser made.
func TestSessionPasskeyServesTheCodeSession(t *testing.T) {
	srv := startServer(t, nil)
	owner := linkBrowser(t, srv)
	secret, _ := enrolTOTP(t, srv, owner)

	code, out := srv.post(t, "/api/passkey/begin", `{}`, owner)
	if code != 403 {
		t.Errorf("a link session's passkey begin = %d (%s), want 403", code, out)
	}

	_, out, cookie := codeSignIn(t, srv, rfc6238(t, secret, nowStep()))
	if cookie == "" {
		t.Fatalf("the code sign-in failed: %s", out)
	}
	device := map[string]string{"Cookie": cookie, "x-huddle-token": ""}

	code, out = srv.post(t, "/api/passkey/begin", `{}`, device)
	if code != 200 {
		t.Fatalf("the code session's passkey begin = %d: %s", code, out)
	}
	var answer struct {
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("the passkey begin body %s: %v", out, err)
	}
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			User      struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(answer.Options, &begin); err != nil {
		t.Fatalf("the passkey begin options %s: %v", answer.Options, err)
	}
	if begin.PublicKey.User.Name != "Mac" {
		t.Errorf("the ceremony enrols user %q, want the browser's own device row", begin.PublicKey.User.Name)
	}

	key := webauthntest.NewSoftKey(t)
	finish, err := json.Marshal(map[string]any{
		"response": key.Create(t, answer.Options, srv.origin()),
	})
	if err != nil {
		t.Fatalf("encode the finish body: %v", err)
	}
	code, out = srv.post(t, "/api/passkey/finish", string(finish), device)
	if code != 200 {
		t.Fatalf("the passkey finish = %d: %s", code, out)
	}
	if _, _, who := srv.get(t, "/api/whoami", device); !strings.Contains(who, `"device_passkey":true`) {
		t.Errorf("the device row holds no passkey after the finish: %s", who)
	}
}
