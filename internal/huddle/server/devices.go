package server

// The devices routes: the paired devices as the owner lists them, and a device's removal —
// the revocation that takes hold at once, because a device's sessions live only while its
// device row does.

import (
	"net/http"
	"net/url"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// devicePath names one device in a removal path; the name arrives percent-encoded when the
// browser kept the spaces a device name may carry.
var devicePath = regexp.MustCompile(`^/api/devices/([^/]+)$`)

// deviceAnswer is one paired device as the caller sees it: the store's view plus whether the
// device answering is this one.
type deviceAnswer struct {
	auth.DeviceView
	Current bool `json:"current"`
}

// devicesRoute answers every paired device, the oldest first, with the caller's own device
// marked current — only a device session names a device; every other credential marks none.
func (s *server) devicesRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	devices, err := s.pairs.Devices(r.Context())
	if err != nil {
		return err
	}
	answers := make([]deviceAnswer, 0, len(devices))
	for _, d := range devices {
		answers = append(answers, deviceAnswer{DeviceView: d, Current: me.Device != "" && d.Name == me.Device})
	}
	s.writeJSON(w, 200, struct {
		Devices []deviceAnswer `json:"devices"`
	}{answers}, nil)
	return nil
}

// deviceRemoveRoute drops one device and every session of it. The removal is a dangerous act:
// it asks for the same fresh passkey assertion an allow or a send asks for, and the
// removal's audit row lands before the answer names it done.
func (s *server) deviceRemoveRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	raw := devicePath.FindStringSubmatch(path)[1]
	name, err := url.PathUnescape(raw)
	if err != nil {
		// a malformed escape names no device that was ever paired
		return &channel.HuddleError{Status: 404, Msg: "no device " + raw}
	}
	if !s.pairs.Remove(r.Context(), name) {
		return &channel.HuddleError{Status: 404, Msg: "no device " + name}
	}
	if _, err := s.audit(r.Context(), auditActor(me), "device.remove", name, nil); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}
