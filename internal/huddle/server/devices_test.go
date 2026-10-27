package server

// The devices routes over the real server: the paired devices as the owner lists
// them, and the removal that needs the same step-up every dangerous act needs. A removed
// device's sessions die with it, so its open browser is signed out on its very next request.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// del runs one DELETE the way the UI does: no body, the route's path naming the action.
func (s *childServer) del(t *testing.T, path string, headers map[string]string) (int, string) {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["x-huddle-token"]; !ok {
		headers["x-huddle-token"] = s.token
	}
	code, _, out, err := s.request(context.Background(), http.MethodDelete, path, "", headers)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return code, out
}

func TestDevicesListsThePairedDevices(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	phoneCookie, _ := pairDevice(t, srv, key, "iPhone")
	padCookie, _ := pairDevice(t, srv, key, "iPad")

	code, _, out := srv.get(t, "/api/devices", deviceHeaders(cookieValueOf(t, phoneCookie)))
	if code != 200 {
		t.Fatalf("GET /api/devices = %d %s, want 200", code, out)
	}
	var list struct {
		Devices []struct {
			Name  string `json:"name"`
			Added string `json:"added"`
			Seen  string `json:"seen"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("devices body %s: %v", out, err)
	}
	if len(list.Devices) != 2 {
		t.Fatalf("the device list holds %d devices, want 2: %s", len(list.Devices), out)
	}
	if list.Devices[0].Name != "iPhone" || list.Devices[1].Name != "iPad" {
		t.Errorf("the device list = %v, want iPhone then iPad", list.Devices)
	}
	for _, d := range list.Devices {
		if d.Added == "" || d.Seen == "" {
			t.Errorf("the device %s carries no added or seen time: %+v", d.Name, d)
		}
	}
	// the machine's own credential reads the same list
	if code, _, out = srv.get(t, "/api/devices", nil); code != 200 {
		t.Errorf("GET /api/devices with the root credential = %d %s, want 200", code, out)
	}
	_ = padCookie
}

func TestRemovingADeviceNeedsAStepUp(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	device := deviceHeaders(cookie)

	// the assertion aged out: the removal is a dangerous act and answers the one 401
	stepUpStamp(t, srv, cookie, time.Now().UnixMilli()-6*60*1000)
	code, out := srv.del(t, "/api/devices/iPhone", device)
	if code != 401 || out != stepUpRequiredBody {
		t.Fatalf("remove on a stale session = %d %s, want 401 %s", code, out, stepUpRequiredBody)
	}
	if _, _, list := srv.get(t, "/api/devices", device); !containsDevice(list, "iPhone") {
		t.Fatalf("the refused removal left the device paired: %s", list)
	}

	// a fresh assertion opens the removal
	if code, out := stepUpOverHTTP(t, srv, key, cookie, userHandle); code != 200 {
		t.Fatalf("stepup finish = %d: %s", code, out)
	}
	if code, out := srv.del(t, "/api/devices/iPhone", device); code != 200 || out != `{"ok":true}` {
		t.Fatalf("remove with the assertion = %d %s, want 200 {\"ok\":true}", code, out)
	}
	if _, _, list := srv.get(t, "/api/devices", nil); containsDevice(list, "iPhone") {
		t.Errorf("the removed device is still listed: %s", list)
	}
	// the removed device's cookie no longer vouches for anything
	if code, _, out := srv.get(t, "/api/devices", device); code != 401 {
		t.Errorf("the removed device's next request = %d %s, want 401", code, out)
	}
}

func TestRemovingAnUnknownDeviceAnswers404(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.del(t, "/api/devices/Ghost", nil); code != 404 || out != `{"error":"no device Ghost"}` {
		t.Fatalf("remove an unknown device = %d %s, want 404 no device Ghost", code, out)
	}
}

func TestDeviceRemovalIsAudited(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	if code, out := stepUpOverHTTP(t, srv, key, cookie, userHandle); code != 200 {
		t.Fatalf("stepup finish = %d: %s", code, out)
	}
	if code, out := srv.del(t, "/api/devices/iPhone", deviceHeaders(cookie)); code != 200 {
		t.Fatalf("remove = %d %s, want 200", code, out)
	}
	code, _, out := srv.get(t, "/api/audit", nil)
	if code != 200 {
		t.Fatalf("the audit log = %d %s", code, out)
	}
	var log struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("audit body %s: %v", out, err)
	}
	for _, e := range log.Entries {
		if e.Action == "device.remove" && e.Actor == "iPhone" && e.Target == "iPhone" {
			return
		}
	}
	t.Errorf("the audit log holds no device.remove row for iPhone: %s", out)
}

// currentFlags reads one devices body into each device's current flag; nil marks a row that
// carries no current field at all.
func currentFlags(t *testing.T, body string) map[string]*bool {
	t.Helper()
	var list struct {
		Devices []struct {
			Name    string `json:"name"`
			Current *bool  `json:"current"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("devices body %s: %v", body, err)
	}
	flags := make(map[string]*bool, len(list.Devices))
	for _, d := range list.Devices {
		flags[d.Name] = d.Current
	}
	return flags
}

func TestDevicesMarksTheCallerOwnDevice(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	phoneCookie, _ := pairDevice(t, srv, key, "iPhone")
	pairDevice(t, srv, key, "iPad")

	code, _, out := srv.get(t, "/api/devices", deviceHeaders(cookieValueOf(t, phoneCookie)))
	if code != 200 {
		t.Fatalf("GET /api/devices = %d %s, want 200", code, out)
	}
	flags := currentFlags(t, out)
	if len(flags) != 2 {
		t.Fatalf("the device list holds %d devices, want 2: %s", len(flags), out)
	}
	if current := flags["iPhone"]; current == nil || !*current {
		t.Errorf("the calling device answers current = %v, want true: %s", current, out)
	}
	if current := flags["iPad"]; current != nil && *current {
		t.Errorf("the other device answers current = true, want false: %s", out)
	}
}

func TestDevicesFromAnOwnerLinkSessionMarksNothingCurrent(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	pairDevice(t, srv, key, "iPhone")
	pairDevice(t, srv, key, "iPad")

	// the owner's link session: the root signs its browser in with a login code, and the
	// browser carries no device of its own
	_, cookie := signInBrowser(t, srv, loginCode(t, srv, map[string]string{"x-huddle-token": srv.token}))
	if cookie == "" {
		t.Fatalf("the owner's sign-in set no cookie")
	}
	code, _, out := srv.get(t, "/api/devices", map[string]string{"Cookie": cookie, "x-huddle-token": ""})
	if code != 200 {
		t.Fatalf("GET /api/devices = %d %s, want 200", code, out)
	}
	for name, current := range currentFlags(t, out) {
		if current == nil {
			t.Errorf("the owner's link session's answer for %s carries no current field: %s", name, out)
		} else if *current {
			t.Errorf("the owner's link session marks %s current, want every row false: %s", name, out)
		}
	}
}

// containsDevice answers whether one JSON device list names the device.
func containsDevice(list, name string) bool {
	var parsed struct {
		Devices []struct {
			Name string `json:"name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(list), &parsed); err != nil {
		return false
	}
	for _, d := range parsed.Devices {
		if d.Name == name {
			return true
		}
	}
	return false
}
