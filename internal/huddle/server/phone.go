package server

// The phone guide over the server: the plain loopback listener serves the local CA's public
// root — the laptop's own browser reads the same bytes the phone downloads — and the owner's
// "Add a phone" starts one plain-HTTP helper on the network the phone shares with this machine,
// its QR drawn by this server. The helper ends with its own deadline or with the server; see
// internal/huddle/phone for the guide itself.

import (
	"context"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// caRoute serves the local CA's public root on the plain listener: DER (PEM when the Accept asks
// for it) and the iOS profile. The root is minted when the home holds none — creating a CA
// installs nothing anywhere.
func (s *server) caRoute(w http.ResponseWriter, r *http.Request, mobile bool) error {
	cert, err := pki.New(home.Home{Root: s.o.Home}.PKI(), pki.Options{}).Root()
	if err != nil {
		return err
	}
	phone.WriteCA(w, r, cert, mobile)
	return nil
}

// phoneStartRoute starts the guide the dashboard's "Add a phone" shows, or answers the one
// already serving: one helper at a time, bound where a phone reaches this machine.
func (s *server) phoneStartRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner may start the phone guide"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	if textOf(fieldOf(body, "action")) != "start" {
		return &channel.HuddleError{Status: 400, Msg: "a phone guide names its action: {\"action\":\"start\"}"}
	}
	cfg, err := home.LoadConfig(home.Home{Root: s.o.Home})
	if err != nil {
		return err
	}
	cert, err := pki.New(home.Home{Root: s.o.Home}.PKI(), pki.Options{}).Root()
	if err != nil {
		return err
	}
	hubURL := ""
	if cfg.Hub.Host != "" {
		hubURL = phone.HubURL(cfg.Hub.Host, cfg.Hub.Listen)
	}
	s.phoneMu.Lock()
	defer s.phoneMu.Unlock()
	if s.phone == nil || !s.phone.Live() {
		// the guide outlives this request and stops with the server (closePhone), so the
		// context here is the process's own, never the request's
		helper, err := phone.Start(context.Background(), phone.HelperOptions{
			Address: phone.BindHost(cfg.Hub.Listen, nil),
			Root:    cert,
			HubURL:  hubURL,
		})
		if err != nil {
			return &channel.HuddleError{Status: 500, Msg: "could not start the phone guide: " + err.Error()}
		}
		s.phone = helper
	}
	s.writeJSON(w, 200, struct {
		URL         string `json:"url"`
		Fingerprint string `json:"fingerprint"`
		ExpiresIn   int64  `json:"expires_in"`
	}{s.phone.URL(), s.phone.Fingerprint(), int64(phone.Life.Seconds())}, nil)
	return nil
}

// phoneQRRoute answers the QR of the running guide as SVG, the picture the dashboard shows. It
// is never stored: the next guide draws a different address.
func (s *server) phoneQRRoute(w http.ResponseWriter, r *http.Request) error {
	s.phoneMu.Lock()
	helper := s.phone
	s.phoneMu.Unlock()
	if helper == nil || !helper.Live() {
		return &channel.HuddleError{Status: 404, Msg: "no phone guide is running: start one first"}
	}
	svg, err := phone.QRSVG(helper.URL())
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(svg)
	return nil
}

// closePhone ends the guide the dashboard started, nil-safe: a run that opened none closes
// nothing.
func (s *server) closePhone() {
	s.phoneMu.Lock()
	helper := s.phone
	s.phoneMu.Unlock()
	if helper != nil {
		helper.Close()
	}
}
