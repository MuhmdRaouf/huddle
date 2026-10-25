package outside

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"time"
)

// ZoneOrigin says where the hub's display zone came from.
type ZoneOrigin string

// The two origins: the first signed-in browser's own zone, or the owner's pick in Settings.
const (
	ZoneFromFirstSignIn ZoneOrigin = "first-sign-in"
	ZoneFromOwner       ZoneOrigin = "owner"
)

// The settings file's members that hold the zone.
const (
	zoneSetting       = `"zone"`
	zoneOriginSetting = `"zone_origin"`
)

// HubZone is the hub's one display time zone: an IANA name and where it came from. The zero
// value is a hub with no zone yet. Storage stays UTC; the zone only names the owner's day.
type HubZone struct {
	Zone       string     `json:"zone"`
	ZoneOrigin ZoneOrigin `json:"zone_origin"`
}

// Location is the zone the hub names its days in: the held zone, or UTC when the hub has none
// or this host cannot load it (Go reads the host's zoneinfo; the binary carries no copy).
func (z HubZone) Location() *time.Location {
	if z.Zone == "" || z.Zone == "Local" {
		return time.UTC
	}
	zone, err := time.LoadLocation(z.Zone)
	if err != nil {
		return time.UTC
	}
	return zone
}

// ZoneError is a zone name Go's time.LoadLocation cannot load.
type ZoneError struct {
	Name string
}

func (e *ZoneError) Error() string {
	return "time zone " + jsonString(e.Name) + " is not an IANA zone name this hub knows (try UTC)"
}

// CheckZone accepts an IANA name time.LoadLocation loads. The empty name and "Local" load too
// (as UTC and this machine's zone), so both are refused: neither names a zone a browser shares.
func CheckZone(name string) error {
	if name == "" || name == "Local" {
		return &ZoneError{Name: name}
	}
	if _, err := time.LoadLocation(name); err != nil {
		return &ZoneError{Name: name}
	}
	return nil
}

// Zones reads and writes the hub's zone in the data dir's settings file, keeping every other
// member in place. The lock makes the first seed the only seed when two browsers race.
type Zones struct {
	dir string
	mu  sync.Mutex
}

// NewZones keeps its zone in dir, the server's data dir.
func NewZones(dir string) *Zones {
	return &Zones{dir: dir}
}

func (z *Zones) file() string { return filepath.Join(z.dir, "settings.json") }

// Get is the zone the settings file holds; a missing or torn member reads as no zone.
func (z *Zones) Get() HubZone {
	z.mu.Lock()
	defer z.mu.Unlock()
	return zoneOf(readSettings(z.file()))
}

// Seed stores name as the first sign-in's zone when the hub has none and name loads; it
// answers the zone the hub holds afterwards.
func (z *Zones) Seed(name string) HubZone {
	z.mu.Lock()
	defer z.mu.Unlock()
	members := readSettings(z.file())
	if held := zoneOf(members); held.Zone != "" || CheckZone(name) != nil {
		return held
	}
	return z.write(members, HubZone{Zone: name, ZoneOrigin: ZoneFromFirstSignIn})
}

// Set stores the owner's pick, refusing a name that does not load.
func (z *Zones) Set(name string) (HubZone, error) {
	if err := CheckZone(name); err != nil {
		return HubZone{}, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.write(readSettings(z.file()), HubZone{Zone: name, ZoneOrigin: ZoneFromOwner}), nil
}

// write puts the zone's two members into members and writes the file; it answers the zone.
func (z *Zones) write(members []settingMember, zone HubZone) HubZone {
	members = putMember(members, zoneSetting, jsonString(zone.Zone))
	members = putMember(members, zoneOriginSetting, jsonString(string(zone.ZoneOrigin)))
	writeSettings(z.dir, z.file(), members)
	return zone
}

// zoneOf reads the zone's members; a zone without a loadable name is no zone.
func zoneOf(members []settingMember) HubZone {
	var zone HubZone
	for _, setting := range members {
		var text string
		if json.Unmarshal([]byte(setting.raw), &text) != nil {
			continue
		}
		switch setting.key {
		case zoneSetting:
			zone.Zone = text
		case zoneOriginSetting:
			zone.ZoneOrigin = ZoneOrigin(text)
		}
	}
	if CheckZone(zone.Zone) != nil {
		return HubZone{}
	}
	if zone.ZoneOrigin != ZoneFromOwner {
		zone.ZoneOrigin = ZoneFromFirstSignIn
	}
	return zone
}

// jsonString is s as a JSON string.
func jsonString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}
