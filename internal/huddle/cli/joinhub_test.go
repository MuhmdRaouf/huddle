package cli

// The satellite words: `huddle pair --satellite` mints the one-use invite and prints the line
// the other machine runs, and `huddle join-hub` spends it at the hub, keeps the credential that
// comes back under the home's keys, and writes the role. The join runs against a fake hub that
// exchanges invites through the same hub.db the invite was minted into — the real exchange, on
// a loopback port this test started.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// TestPairSatellitePrintsTheJoinLine pins the hub's half of the pairing: one line, one invite,
// one expiry, and the machine's name on it.
func TestPairSatellitePrintsTheJoinLine(t *testing.T) {
	homeDir, w := oneHome(t)
	if _, err := home.SavePort(home.Home{Root: homeDir}, 18999, false); err != nil {
		t.Fatalf("save the port: %v", err)
	}
	code, out, errOut := pairOut(t, w, []string{"pair", "--satellite", "--name", "studio"})
	if code != 0 {
		t.Fatalf("huddle pair --satellite = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "one use") || !strings.Contains(out, "10 minutes") {
		t.Errorf("the invite's terms are missing:\n%s", out)
	}
	if regexp.MustCompile(`huddle join-hub http://localhost:18999 (hsat_[A-Za-z0-9_-]{20,64})`).FindStringSubmatch(out) == nil {
		t.Fatalf("no join line on the terminal:\n%s", out)
	}
	if code, _, errOut := pairOut(t, w, []string{"pair", "--satellite", "--name", "bad name!"}); code != 2 || errOut == "" {
		t.Errorf("a bad machine name = %d (%q), want 2 with a usage line", code, errOut)
	}
}

// TestJoinHubSpendsTheInviteAndKeepsTheCredential runs the satellite's word end to end: the
// invite becomes the machine's credential, the config names the role and the hub, and a spent
// invite joins nothing twice.
func TestJoinHubSpendsTheInviteAndKeepsTheCredential(t *testing.T) {
	homeDir, w := oneHome(t)
	store := &fakeSecrets{}
	w.Secrets = func(context.Context, home.Home) home.SecretStore { return store }
	if _, err := home.SavePort(home.Home{Root: homeDir}, 18999, false); err != nil {
		t.Fatalf("save the port: %v", err)
	}
	_, minted, errOut := pairOut(t, w, []string{"pair", "--satellite", "--name", "studio"})
	if errOut != "" {
		t.Fatalf("pair --satellite: %s", errOut)
	}
	invite := regexp.MustCompile(`hsat_[A-Za-z0-9_-]{20,64}`).FindString(minted)
	if invite == "" {
		t.Fatalf("no invite minted:\n%s", minted)
	}

	// the fake hub exchanges invites through the same hub.db the invite lives in
	exchanges := 0
	hubDB := filepath.Join(homeDir, "hub.db")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sat/join" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		db, err := hub.Open(context.Background(), hubDB)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer func() { _ = db.Close() }()
		var body struct {
			Invite string `json:"invite"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Invite == "" || body.Name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		credential, err := db.ExchangeSatellite(r.Context(), body.Invite, body.Name, time.Now().UnixMilli())
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or expired satellite invite"}`))
			return
		}
		exchanges++
		_, _ = w.Write([]byte(`{"credential":"` + credential + `","name":"` + body.Name + `"}`))
	}))
	defer fake.Close()

	code, out, errOut := pairOut(t, w, []string{"join-hub", fake.URL, invite, "--name", "studio"})
	if code != 0 {
		t.Fatalf("huddle join-hub = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "joined "+fake.URL+" as studio") {
		t.Errorf("the join's word is missing:\n%s", out)
	}
	cfg, err := home.LoadConfig(home.Home{Root: homeDir})
	if err != nil || cfg.Role != "satellite" || cfg.Hub.URL != fake.URL || cfg.Machine != "studio" {
		t.Errorf("the config after the join = %+v, %v", cfg, err)
	}
	kept, err := home.LoadSatelliteSecret(context.Background(), store, fake.URL)
	if err != nil || !auth.SatelliteRe.MatchString(kept) {
		t.Errorf("the kept credential = %q, %v", kept, err)
	}
	if exchanges != 1 {
		t.Errorf("the hub exchanged %d invites, want 1", exchanges)
	}

	// the invite was one use: a second join is refused and changes nothing
	code, _, errOut = pairOut(t, w, []string{"join-hub", fake.URL, invite})
	if code != 2 || !strings.Contains(errOut, "invalid or expired") {
		t.Errorf("a spent invite = %d (%q), want 2 with the hub's refusal", code, errOut)
	}

	// usage: a missing invite is a usage line, never a request
	if code, _, errOut := pairOut(t, w, []string{"join-hub", fake.URL}); code != 2 || errOut == "" {
		t.Errorf("join-hub with no invite = %d (%q), want 2", code, errOut)
	}
}

// TestJoinHubKeepsTheCredentialInTheSecretStore pins where the credential rests: in
// the secret store under the hub's account, and nowhere under the home's keys/.
func TestJoinHubKeepsTheCredentialInTheSecretStore(t *testing.T) {
	homeDir, w := oneHome(t)
	store := &fakeSecrets{}
	w.Secrets = func(context.Context, home.Home) home.SecretStore { return store }
	if _, err := home.SavePort(home.Home{Root: homeDir}, 18999, false); err != nil {
		t.Fatalf("save the port: %v", err)
	}
	_, minted, errOut := pairOut(t, w, []string{"pair", "--satellite", "--name", "studio"})
	if errOut != "" {
		t.Fatalf("pair --satellite: %s", errOut)
	}
	invite := regexp.MustCompile(`hsat_[A-Za-z0-9_-]{20,64}`).FindString(minted)
	if invite == "" {
		t.Fatalf("no invite minted:\n%s", minted)
	}

	// the fake hub exchanges invites through the same hub.db the invite lives in
	hubDB := filepath.Join(homeDir, "hub.db")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		db, err := hub.Open(context.Background(), hubDB)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer func() { _ = db.Close() }()
		var body struct {
			Invite string `json:"invite"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		credential, err := db.ExchangeSatellite(r.Context(), body.Invite, body.Name, time.Now().UnixMilli())
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or expired satellite invite"}`))
			return
		}
		_, _ = w.Write([]byte(`{"credential":"` + credential + `","name":"` + body.Name + `"}`))
	}))
	defer fake.Close()

	if code, _, errOut := pairOut(t, w, []string{"join-hub", fake.URL, invite, "--name", "studio"}); code != 0 {
		t.Fatalf("huddle join-hub = %d: %s", code, errOut)
	}
	account := home.SatelliteSecretName(fake.URL)
	if kept, ok := store.held[account]; !ok || !auth.SatelliteRe.MatchString(kept) {
		t.Fatalf("the store holds %q under %q, want the minted credential", kept, account)
	}
	if _, err := os.Stat(home.Home{Root: homeDir}.Keys()); !os.IsNotExist(err) {
		t.Errorf("the keys directory = %v, want nothing written there", err)
	}
}
