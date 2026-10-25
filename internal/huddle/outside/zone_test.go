package outside

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestZones(t *testing.T) {
	t.Run("a fresh home has no zone", func(t *testing.T) {
		if got := NewZones(t.TempDir()).Get(); got != (HubZone{}) {
			t.Errorf("Get() = %+v, want the zero zone", got)
		}
	})

	t.Run("the first seed wins and a later one never overwrites", func(t *testing.T) {
		z := NewZones(t.TempDir())
		want := HubZone{Zone: "Europe/Istanbul", ZoneOrigin: ZoneFromFirstSignIn}
		if got := z.Seed("Europe/Istanbul"); got != want {
			t.Errorf("Seed = %+v, want %+v", got, want)
		}
		if got := z.Seed("America/New_York"); got != want {
			t.Errorf("a second Seed = %+v, want %+v", got, want)
		}
	})

	t.Run("a name Go cannot load seeds nothing", func(t *testing.T) {
		z := NewZones(t.TempDir())
		for _, name := range []string{"", "Local", "Mars/Olympus_Mons", "../etc/passwd"} {
			if got := z.Seed(name); got != (HubZone{}) {
				t.Errorf("Seed(%q) = %+v, want none", name, got)
			}
		}
	})

	t.Run("the owner's change replaces the zone and is refused for a bad name", func(t *testing.T) {
		z := NewZones(t.TempDir())
		z.Seed("Europe/Istanbul")
		got, err := z.Set("Asia/Tokyo")
		if err != nil || got != (HubZone{Zone: "Asia/Tokyo", ZoneOrigin: ZoneFromOwner}) {
			t.Errorf("Set = %+v, %v", got, err)
		}
		var bad *ZoneError
		if _, err := z.Set("Mars/Olympus_Mons"); !errors.As(err, &bad) || bad.Name != "Mars/Olympus_Mons" {
			t.Errorf("Set of an unknown zone = %v, want a ZoneError", err)
		}
		if got := z.Get(); got.Zone != "Asia/Tokyo" {
			t.Errorf("a refused Set changed the zone to %+v", got)
		}
		if got := z.Seed("Europe/Paris"); got.Zone != "Asia/Tokyo" {
			t.Errorf("a seed after the owner's change = %+v, want it kept", got)
		}
	})

	t.Run("the zone and the notify switch share the settings file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		NewNotifier(dir).Set(false)
		NewZones(dir).Seed("UTC")
		want := "{\n  \"notify\": false,\n  \"zone\": \"UTC\",\n  \"zone_origin\": \"first-sign-in\"\n}\n"
		if got, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(got) != want {
			t.Errorf("settings.json = %q, want %q", got, want)
		}
		NewNotifier(dir).Set(true)
		if got := NewZones(dir).Get(); got.Zone != "UTC" {
			t.Errorf("the notify switch dropped the zone: %+v", got)
		}
	})
}
