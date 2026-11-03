package outside

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// TestSecretsForKeepsEverySecretInTheHomesKeyFilesWhenPinnedToFiles: HUDDLE_KEYS=files pins the
// store to the home's keys/ files without asking any OS keystore, so a hermetic run's secrets
// stay under its temp home.
func TestSecretsForKeepsEverySecretInTheHomesKeyFilesWhenPinnedToFiles(t *testing.T) {
	t.Setenv("HUDDLE_KEYS", "files")
	h := home.Home{Root: t.TempDir()}
	ctx := context.Background()

	store := SecretsFor(ctx, h)
	if err := store.Set(ctx, "dns:cloudflare", "token-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, ok := store.Get(ctx, "dns:cloudflare"); !ok || got != "token-value" {
		t.Errorf("Get = (%q, %v), want the secret back", got, ok)
	}
	kept, err := os.ReadFile(filepath.Join(h.Keys(), "dns-cloudflare"))
	if err != nil || string(kept) != "token-value" {
		t.Errorf("the key file = (%q, %v), want the secret under the home's keys/", kept, err)
	}
}

// TestAZoneErrorNamesTheZoneAndOffersUTC: the refusal for a zone Go cannot load quotes the name
// as JSON and points at UTC.
func TestAZoneErrorNamesTheZoneAndOffersUTC(t *testing.T) {
	err := CheckZone(`Mars/"Olympus"`)
	var bad *ZoneError
	if !errors.As(err, &bad) {
		t.Fatalf("CheckZone = %v, want a *ZoneError", err)
	}
	want := `time zone "Mars/\"Olympus\"" is not an IANA zone name this hub knows (try UTC)`
	if err.Error() != want {
		t.Errorf("the message = %q, want %q", err.Error(), want)
	}
}

// TestTheACMEAccountIsIdentifiedByItsKeyAlone: the account lego talks to Let's Encrypt as has no
// email, and hands back the registration and the key it was built with.
func TestTheACMEAccountIsIdentifiedByItsKeyAlone(t *testing.T) {
	key := struct{ id string }{"account-key"}
	user := &acmeUser{key: key}
	if user.GetEmail() != "" {
		t.Errorf("GetEmail = %q, want none", user.GetEmail())
	}
	if user.GetRegistration() != nil {
		t.Error("an account that never registered has a registration")
	}
	if user.GetPrivateKey() != key {
		t.Errorf("GetPrivateKey = %v, want the account key", user.GetPrivateKey())
	}
}
