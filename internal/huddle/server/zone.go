package server

import (
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// ZoneHeader carries the browser's IANA time zone on its requests until an answer carries it back;
// the first owner request that carries a loadable one seeds the hub's display zone. The answer
// names the zone the hub holds (empty when it has none), so the browser knows it was heard.
const ZoneHeader = "x-huddle-zone"

// seedZone gives the hub its display zone from the owner's browser the first time one sends it;
// a hub that has a zone, a member's request and a name Go cannot load all leave it as it is.
func (s *server) seedZone(w http.ResponseWriter, r *http.Request, me auth.Who) {
	name := r.Header.Get(ZoneHeader)
	if name == "" || me.Name != channel.Owner || (me.Kind != auth.KindOwner && me.Kind != auth.KindDevice) {
		return
	}
	w.Header().Set(ZoneHeader, s.zones.Seed(name).Zone)
}
