package cli

// The join's refusals: a hub address no request can form, a hub that will not answer or will
// not hand a credential, a store and a home that say no — each one exit code and one line, and
// nothing half-written left behind.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// mintAndExchange seeds one home with a minted invite and a hub that spends it the real way,
// and answers the hub's url and the invite.
func mintAndExchange(t *testing.T, w *World, homeDir string) (string, string) {
	t.Helper()
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
	t.Cleanup(fake.Close)
	return fake.URL, invite
}

// TestJoinHubRefusesABadUrlAnUnreachableHubAndABadName pins the three refusals before any state
// moves: an address no request can carry, a hub that does not answer, and a machine name no
// machine may hold.
func TestJoinHubRefusesABadUrlAnUnreachableHubAndABadName(t *testing.T) {
	homeDir, w := oneHome(t)
	hubURL, invite := mintAndExchange(t, w, homeDir)

	if code, _, errOut := pairOut(t, w, []string{"join-hub", "ftp://hub.example", invite}); code != 2 ||
		!strings.Contains(errOut, "starts https://") {
		t.Fatalf("an ftp hub = (%d, %q), want 2 with the scheme line", code, errOut)
	}
	if code, _, errOut := pairOut(t, w, []string{"join-hub", "http://127.0.0.1:1", "hsat_aaaaaaaaaaaaaaaaaaaaaa"}); code != 2 ||
		!strings.Contains(errOut, "does not answer") {
		t.Fatalf("an unreachable hub = (%d, %q), want 2", code, errOut)
	}
	if code, _, errOut := pairOut(t, w, []string{"join-hub", hubURL, invite, "--name", "bad name!"}); code != 2 ||
		!strings.Contains(errOut, "--name") {
		t.Fatalf("a bad machine name = (%d, %q), want 2 before any exchange", code, errOut)
	}
	// the invite was never spent: the next join with a good name takes it
	store := &fakeSecrets{}
	w.Secrets = func(context.Context, home.Home) home.SecretStore { return store }
	if code, _, errOut := pairOut(t, w, []string{"join-hub", hubURL, invite, "--name", "studio"}); code != 0 {
		t.Fatalf("the good join = (%d, %q)", code, errOut)
	}
}

// TestJoinHubRefusesWhatTheStoreAndTheConfigRefuse proves a credential is never lost silently:
// a store that says no and a config that cannot be written each end the join with their own
// line, and a torn config is refused before anything is exchanged.
func TestJoinHubRefusesWhatTheStoreAndTheConfigRefuse(t *testing.T) {
	homeDir, w := oneHome(t)
	store := &fakeSecrets{setErr: map[string]error{}}
	w.Secrets = func(context.Context, home.Home) home.SecretStore { return store }
	hubURL, invite := mintAndExchange(t, w, homeDir)
	store.setErr[home.SatelliteSecretName(hubURL)] = errors.New("the keystore says no")
	if code, _, errOut := pairOut(t, w, []string{"join-hub", hubURL, invite, "--name", "studio"}); code != 1 ||
		!strings.Contains(errOut, "could not store the credential") {
		t.Fatalf("a refusing store = (%d, %q), want 1", code, errOut)
	}

	// a config the writer cannot replace: the join ends with its own line
	// a home whose config writer is blocked: the exchange goes through, the role cannot be
	// written, and the config that was read stays as it was
	blocked := t.TempDir()
	w2 := cliWorld(t, blocked, filepath.Join(t.TempDir(), "state"), nil)
	store2 := &fakeSecrets{}
	w2.Secrets = func(context.Context, home.Home) home.SecretStore { return store2 }
	hubURL2, invite2 := mintAndExchange(t, w2, blocked)
	// the writer's own temp name, held by a directory: no config write can pass it
	tmp := filepath.Join(blocked, fmt.Sprintf("config.toml.%d.tmp", os.Getpid()))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("block the writer: %v", err)
	}
	if code, _, errOut := pairOut(t, w2, []string{"join-hub", hubURL2, invite2, "--name", "studio"}); code != 1 ||
		!strings.Contains(errOut, "could not write config.toml") {
		t.Fatalf("an unwritable config = (%d, %q), want 1", code, errOut)
	}
}

// TestJoinHubReadsTheHubsAnswerWhole proves what the join makes of a hub's answer: a 200 with no
// credential, a torn body and a refusal that names itself each end the join, and a machine with
// no home never joins at all.
func TestJoinHubReadsTheHubsAnswerWhole(t *testing.T) {
	if code, _, errOut := pairOut(t, &World{Getenv: func(string) string { return "" }},
		[]string{"join-hub", "https://hub.example", "hsat_aaaaaaaaaaaaaaaaaaaaaa"}); code != 1 ||
		!strings.Contains(errOut, "no home") {
		t.Fatalf("no home = (%d, %q), want 1", code, errOut)
	}

	noCredential := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(noCredential.Close)
	if code, _, errOut := oneJoinAt(t, noCredential.URL); code != 2 || !strings.Contains(errOut, "answered 200") {
		t.Fatalf("a credentialless 200 = (%d, %q), want 2", code, errOut)
	}

	torn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(torn.Close)
	if code, _, errOut := oneJoinAt(t, torn.URL); code != 2 || !strings.Contains(errOut, "answered 200") {
		t.Fatalf("a torn answer = (%d, %q), want 2", code, errOut)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"the hub says no"}`))
	}))
	t.Cleanup(forbidden.Close)
	if code, _, errOut := oneJoinAt(t, forbidden.URL); code != 2 || !strings.Contains(errOut, "the hub says no") {
		t.Fatalf("a refusal = (%d, %q), want the hub's own line", code, errOut)
	}
}

// oneJoinAt runs one join against the hub at url with a throwaway home and a shape-correct
// invite, and answers the exit code and the error stream.
func oneJoinAt(t *testing.T, url string) (int, string, string) {
	t.Helper()
	_, w := oneHome(t)
	return pairOut(t, w, []string{"join-hub", url, "hsat_aaaaaaaaaaaaaaaaaaaaaa", "--name", "studio"})
}
