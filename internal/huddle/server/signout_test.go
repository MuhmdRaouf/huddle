package server

// Signing out over the API: one route ends this browser's session — a link session or a paired
// device's — the other ends every browser and device session at once behind a fresh step-up,
// and neither ends the paired devices, the root token or a member's own credential.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// hasCookieNamed says whether one of the Set-Cookie lines names that cookie.
func hasCookieNamed(lines []string, name string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, name+"=") {
			return true
		}
	}
	return false
}

// auditActions answers every action the audit log holds.
func auditActions(t *testing.T, srv *childServer) map[string]bool {
	t.Helper()
	_, _, body := srv.get(t, "/api/audit", nil)
	seen := map[string]bool{}
	entries, _ := bodyMap(t, body)["entries"].([]any)
	for _, entry := range entries {
		row, _ := entry.(map[string]any)
		action, _ := row["action"].(string)
		seen[action] = true
	}
	return seen
}

func TestSignOutEndsThisBrowserOnly(t *testing.T) {
	srv := startServer(t, nil)
	desktop := linkBrowser(t, srv)
	laptop := linkBrowser(t, srv)

	write := map[string]string{"Cookie": desktop["Cookie"], "content-type": "application/json"}
	code, header, body, err := srv.request(context.Background(), http.MethodPost, "/api/signout", "", write)
	if err != nil {
		t.Fatalf("POST /api/signout: %v", err)
	}
	if code != 200 {
		t.Fatalf("POST /api/signout = %d, want 200: %s", code, body)
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("the sign-out body = %s, want ok true", body)
	}

	// both credentials this browser may carry get their clearing cookie on the one answer
	clearing := header.Values("Set-Cookie")
	if len(clearing) != 2 {
		t.Fatalf("the sign-out set %d cookies (%v), want the link and the device ones cleared", len(clearing), clearing)
	}
	linkName, _, _ := strings.Cut(desktop["Cookie"], "=")
	for _, line := range clearing {
		if !strings.Contains(line, "Max-Age=0") {
			t.Errorf("the clearing cookie %q does not expire the session", line)
		}
	}
	if !hasCookieNamed(clearing, linkName) {
		t.Errorf("the clearing cookies %v leave the link cookie %s alive", clearing, linkName)
	}
	if !hasCookieNamed(clearing, auth.PairPlainCookie) {
		t.Errorf("the clearing cookies %v leave the device cookie alive", clearing)
	}

	// the replayed credential is refused like a signed-out browser, and only this browser ended
	code, _, body, _ = srv.request(context.Background(), http.MethodGet, "/api/devices", "", desktop)
	if code != 401 || !strings.Contains(body, `"signin":true`) {
		t.Errorf("the signed-out browser's replay = (%d, %s), want 401 naming the sign-in", code, body)
	}
	if code, _, body := srv.get(t, "/api/devices", laptop); code != 200 {
		t.Errorf("the other browser's read = %d, want it still signed in: %s", code, body)
	}
	if !auditActions(t, srv)["signout"] {
		t.Error("the audit log holds no signout row for the browser's sign-out")
	}
}

func TestSignOutEndsADeviceSession(t *testing.T) {
	srv := startServer(t, nil)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "phone")
	phone := deviceHeaders(cookieValueOf(t, setCookie))

	if code, body := srv.post(t, "/api/signout", "", phone); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("POST /api/signout = (%d, %s), want 200 ok true", code, body)
	}
	code, _, body, _ := srv.request(context.Background(), http.MethodGet, "/api/devices", "", phone)
	if code != 401 || !strings.Contains(body, `"signin":true`) {
		t.Errorf("the signed-out device's replay = (%d, %s), want 401 naming the sign-in", code, body)
	}
	// ending the session is not unpairing
	if code, _, body := srv.get(t, "/api/devices", nil); code != 200 || !strings.Contains(body, "phone") {
		t.Errorf("the devices after the sign-out = (%d, %s), want phone still paired", code, body)
	}
}

func TestSignOutAllNeedsAFreshStepUpFromADevice(t *testing.T) {
	srv := startServer(t, nil)
	browser := linkBrowser(t, srv)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "phone")
	cookie := cookieValueOf(t, setCookie)
	phone := deviceHeaders(cookie)
	stepUpStamp(t, srv, cookie, time.Now().Add(-10*time.Minute).UnixMilli())

	if code, body := srv.post(t, "/api/signout/all", "", phone); code != 401 || body != stepUpRequiredBody {
		t.Fatalf("POST /api/signout/all with a stale step-up = (%d, %s), want 401 %q", code, body, stepUpRequiredBody)
	}
	// nothing ended while the sweep was refused
	if code, _, body := srv.get(t, "/api/devices", browser); code != 200 {
		t.Errorf("the browser's read after the refusal = %d, want it still signed in: %s", code, body)
	}
	if code, _, body := srv.get(t, "/api/devices", phone); code != 200 {
		t.Errorf("the device's read after the refusal = %d, want its session still live: %s", code, body)
	}
}

func TestSignOutAllEndsEveryBrowserAndDeviceSession(t *testing.T) {
	srv := startServer(t, nil)
	desktop := linkBrowser(t, srv)
	laptop := linkBrowser(t, srv)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "phone")
	phone := deviceHeaders(cookieValueOf(t, setCookie))

	if code, body := srv.post(t, "/api/signout/all", "", phone); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("POST /api/signout/all = (%d, %s), want 200 ok true", code, body)
	}
	for name, who := range map[string]map[string]string{"desktop": desktop, "laptop": laptop, "phone": phone} {
		code, _, body, _ := srv.request(context.Background(), http.MethodGet, "/api/devices", "", who)
		if code != 401 || !strings.Contains(body, `"signin":true`) {
			t.Errorf("%s's session after the sweep = (%d, %s), want 401 naming the sign-in", name, code, body)
		}
	}
	// every session ended, nothing unpaired
	if code, _, body := srv.get(t, "/api/devices", nil); code != 200 || !strings.Contains(body, "phone") {
		t.Errorf("the devices after the sweep = (%d, %s), want phone still paired", code, body)
	}
	if seen := auditActions(t, srv); !seen["signout.all"] {
		t.Errorf("the audit actions %v hold no signout.all row for the sweep", seen)
	}
}

func TestASignOutTheAuditCannotRecordHappensNowhere(t *testing.T) {
	srv := startServerWithoutRegistry(t)
	write := map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"}
	for _, path := range []string{"/api/signout", "/api/signout/all"} {
		code, _, body, err := srv.request(context.Background(), http.MethodPost, path, "", write)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		if code != 500 || !strings.Contains(body, "the registry's database is unavailable") {
			t.Errorf("POST %s = (%d, %s), want 500 refusing to act unaudited", path, code, body)
		}
	}
	// the credential that asked still answers a read: nothing ended anywhere
	if code, _, body := srv.get(t, "/api/audit", nil); code != 500 {
		t.Errorf("the audit read after the refusals = %d, want the same broken registry: %s", code, body)
	}
}
