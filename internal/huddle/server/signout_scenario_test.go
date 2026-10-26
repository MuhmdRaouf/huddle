package server

// The sign-out everywhere behaviour the Settings Security page offers: one act from one device
// ends every browser and device session, and the devices themselves stay paired.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestSignOutScenario(t *testing.T) {
	bdd.Feature(t, "signing out of the hub", func(f *bdd.F) {
		f.Scenario("sign out everywhere ends every browser and keeps the phone paired", func(s *bdd.S) {
			var srv *childServer
			var desktop, laptop map[string]string
			var phone map[string]string

			s.Given("a hub with a paired phone and two signed-in browsers", func(_ *testing.T) {
				// the hub and its sessions take the scenario's s.T: the server and its stop
				// outlive every step
				srv = startServer(s.T, nil)
				desktop = linkBrowser(s.T, srv)
				laptop = linkBrowser(s.T, srv)
				setCookie, _ := pairDevice(s.T, srv, webauthntest.NewSoftKey(s.T), "phone")
				phone = deviceHeaders(cookieValueOf(s.T, setCookie))
			})
			s.When("the phone signs out everywhere", func(t *testing.T) {
				code, body := srv.post(t, "/api/signout/all", "", phone)
				if code != 200 || !strings.Contains(body, `"ok":true`) {
					t.Fatalf("POST /api/signout/all = (%d, %s), want 200 ok true", code, body)
				}
			})
			s.Then("both browsers' old cookies are refused", func(t *testing.T) {
				for name, who := range map[string]map[string]string{"desktop": desktop, "laptop": laptop} {
					code, _, body, _ := srv.request(context.Background(), http.MethodGet, "/api/devices", "", who)
					if code != 401 || !strings.Contains(body, `"signin":true`) {
						t.Errorf("%s's session after the sweep = (%d, %s), want 401 naming the sign-in", name, code, body)
					}
				}
			})
			s.And("the phone's old session is refused too", func(t *testing.T) {
				code, _, body, _ := srv.request(context.Background(), http.MethodGet, "/api/devices", "", phone)
				if code != 401 || !strings.Contains(body, `"signin":true`) {
					t.Errorf("the phone's session after the sweep = (%d, %s), want 401 naming the sign-in", code, body)
				}
			})
			s.And("the phone is still paired", func(t *testing.T) {
				code, _, body := srv.get(t, "/api/devices", nil)
				if code != 200 || !strings.Contains(body, "phone") {
					t.Errorf("the devices after the sweep = (%d, %s), want phone still paired", code, body)
				}
			})
			s.And("the sweep is on the audit record", func(t *testing.T) {
				if !auditActions(t, srv)["signout.all"] {
					t.Error("the audit log holds no signout.all row for the sweep")
				}
			})
		})
	})
}
