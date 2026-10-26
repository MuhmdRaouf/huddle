// File reservations: a session claims the paths it is about to change, and another session that
// reaches for one of them is told who holds it — before the edit, never instead of it. One row
// per path; a directory reservation covers everything under it. Matching is on cleaned absolute
// paths. A reservation may also report the repository it lives in — its identity (the normalised
// origin, the way a board's key folds every clone of one repository into one) and the checkout
// root its paths are relative to; a query that reports the same identity matches on the path
// inside the repository, so another checkout or another machine of the same repository is
// recognised. Without that information the absolute paths are all there is. Nothing is ever
// blocked: a taken path is an answer, not an error, and the PreToolUse warning is context,
// never a deny.
package channel

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// DefaultTTLMin is how long a reservation holds when the call names no ttl.
const DefaultTTLMin = 60

// MaxTTLMin is the longest a reservation holds, so a session that vanishes cannot hold a path
// forever.
const MaxTTLMin = 480

// ReserveArgs is one reserve call: the absolute paths to claim, for how long, and why. Repo and
// Root carry the repository's identity and the checkout root the paths are relative to, when
// the caller knows them.
type ReserveArgs struct {
	Paths      []string
	TTLMinutes *int64
	Note       *string
	Repo       *string
	Root       *string
}

// ReservePath is one path's answer.
type ReservePath struct {
	Path   string  `json:"path"`
	Held   bool    `json:"held"`
	Holder *string `json:"holder,omitempty"`
	Until  *string `json:"until,omitempty"`
}

// ReserveResult is one reserve's answer, one entry per path asked for.
type ReserveResult struct {
	Paths []ReservePath `json:"paths"`
}

// ReleaseResult is one release's answer: the paths that were freed.
type ReleaseResult struct {
	Released []string `json:"released"`
}

// ReservationView is one live reservation.
type ReservationView struct {
	Path    string  `json:"path"`
	Holder  string  `json:"holder"`
	Note    *string `json:"note,omitempty"`
	Created string  `json:"created"`
	Expires string  `json:"expires"`
}

// ReservationsView is the list the channel page shows.
type ReservationsView struct {
	Reservations []ReservationView `json:"reservations"`
}

// ReservedResult is the PreToolUse lookup's answer.
type ReservedResult struct {
	Held   bool    `json:"held"`
	Path   string  `json:"path"`
	Holder *string `json:"holder,omitempty"`
	Until  *string `json:"until,omitempty"`
	Note   *string `json:"note,omitempty"`
}

// holderOf answers the session a name holds for: a subagent's claims and lookups are its
// session's, the way its edits are.
func holderOf(me string) string {
	if parent, ok := ParentOf(me); ok {
		return parent
	}
	return me
}

// cleanReservePath cleans one path a session reserves or asks about: absolute, forward slashes,
// no duplicate. A relative path is refused: the caller knows its own working directory.
func cleanReservePath(p string) (string, error) {
	p = path.Clean(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"))
	if p == "" || p == "." || !strings.HasPrefix(p, "/") {
		return "", bad("path: an absolute path, as the working tree sees it")
	}
	if strings.ContainsRune(p, 0) || jsscalar.UTF16Len(p) > 500 {
		return "", bad("path: an absolute path, as the working tree sees it")
	}
	return p, nil
}

// relOf answers p's path inside root, "" when root is empty or p is not under it.
func relOf(p, root string) string {
	if root == "" {
		return ""
	}
	if p == root {
		return ""
	}
	if rel, ok := strings.CutPrefix(p, root+"/"); ok {
		return rel
	}
	return ""
}

// sweepTx deletes the rows that no longer hold: the expired ones and the rows of sessions that
// left (the owner keeps its own). Every reservation read runs it first.
func sweepTx(ctx context.Context, q store.Q, now string) error {
	return q.Run(ctx, `DELETE FROM reservations
      WHERE expires <= ? OR (holder != 'owner' AND holder NOT IN (SELECT name FROM sessions WHERE state != 'left'))`, now)
}

// liveRows answers the reservations that hold right now, newest first.
func liveRows(ctx context.Context, q store.Q, holder string) ([]Row, error) {
	query := "SELECT path, holder, note, created, expires FROM reservations"
	params := []any{}
	if holder != "" {
		query += " WHERE holder = ?"
		params = append(params, holder)
	}
	return q.All(ctx, query+" ORDER BY rowid DESC", params...)
}

// Reserve claims each path that no other live session holds, and answers per path: held, or
// taken by whom until when. A path the caller already holds is extended. A claim (and only a
// claim) puts one reserve.claim event on the timeline.
func Reserve(ctx context.Context, c *Channel, me string, a ReserveArgs) (ReserveResult, error) {
	if len(a.Paths) == 0 {
		return ReserveResult{}, bad("reserve: need paths")
	}
	paths := []string{}
	for _, p := range a.Paths {
		clean, err := cleanReservePath(p)
		if err != nil {
			return ReserveResult{}, err
		}
		if !slices.Contains(paths, clean) {
			paths = append(paths, clean)
		}
	}
	ttl := int64(DefaultTTLMin)
	if a.TTLMinutes != nil {
		ttl = max(1, min(MaxTTLMin, *a.TTLMinutes))
	}
	repo := ""
	if a.Repo != nil {
		repo = strings.TrimSpace(*a.Repo)
	}
	root := ""
	if a.Root != nil {
		root = path.Clean(strings.TrimSpace(*a.Root))
	}
	name := holderOf(me)
	var out ReserveResult
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, name); err != nil {
			return err
		}
		out = ReserveResult{Paths: []ReservePath{}}
		var claimed []string
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			now := c.nowISO()
			if err := sweepTx(ctx, q, now); err != nil {
				return err
			}
			expires := isoOfMs(c.nowMs() + ttl*60_000)
			for _, p := range paths {
				row, err := q.Get(ctx, "SELECT holder, expires FROM reservations WHERE path=?", p)
				if err != nil {
					return err
				}
				if row != nil && rStr(row, "holder") != name {
					holder := rStr(row, "holder")
					until := rStr(row, "expires")
					out.Paths = append(out.Paths, ReservePath{Path: p, Holder: &holder, Until: &until})
					continue
				}
				if err := q.Run(ctx, `INSERT INTO reservations (path, repo, rel, holder, note, created, expires)
			      VALUES (?,?,?,?,?,?,?)
			      ON CONFLICT(path) DO UPDATE SET expires=excluded.expires, note=excluded.note,
			        repo=excluded.repo, rel=excluded.rel, holder=excluded.holder, created=excluded.created`,
					p, repo, relOf(p, root), name, derefOrNil(a.Note), now, expires); err != nil {
					return err
				}
				out.Paths = append(out.Paths, ReservePath{Path: p, Held: true})
				claimed = append(claimed, p)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(claimed) > 0 {
			msg := utf16Cut(fmt.Sprintf("%s reserved %s", name, strings.Join(claimed, ", ")), 300)
			if _, err := c.append(ctx, name, evIn{
				topic: "reserve.claim", msg: sp(msg), data: reserveData(claimed),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// reserveData is the events' data: which paths moved.
func reserveData(paths []string) *jsfmt.Value {
	items := make([]*jsfmt.Value, len(paths))
	for i, p := range paths {
		items[i] = jstr(p)
	}
	return jsfmt.Object([]string{"paths"}, []*jsfmt.Value{jsfmt.Array(items)})
}

// Release frees the paths named, or every path the caller holds when none are. Paths another
// session holds, or nobody does, are not the caller's to free and answer nothing.
func Release(ctx context.Context, c *Channel, me string, paths []string) (ReleaseResult, error) {
	name := holderOf(me)
	var released []string
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, name); err != nil {
			return err
		}
		released = nil
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			now := c.nowISO()
			if err := sweepTx(ctx, q, now); err != nil {
				return err
			}
			if len(paths) == 0 {
				rows, err := liveRows(ctx, q, name)
				if err != nil {
					return err
				}
				for _, r := range rows {
					released = append(released, rStr(r, "path"))
				}
				if len(released) > 0 {
					return q.Run(ctx, "DELETE FROM reservations WHERE holder=?", name)
				}
				return nil
			}
			for _, p := range paths {
				clean, err := cleanReservePath(p)
				if err != nil {
					return err
				}
				row, err := q.Get(ctx, "SELECT holder FROM reservations WHERE path=?", clean)
				if err != nil {
					return err
				}
				if row == nil || rStr(row, "holder") != name {
					continue
				}
				if err := q.Run(ctx, "DELETE FROM reservations WHERE path=?", clean); err != nil {
					return err
				}
				released = append(released, clean)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(released) > 0 {
			msg := utf16Cut(fmt.Sprintf("%s released %s", name, strings.Join(released, ", ")), 300)
			if _, err := c.append(ctx, name, evIn{
				topic: "reserve.release", msg: sp(msg), data: reserveData(released),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ReleaseResult{}, err
	}
	return ReleaseResult{Released: released}, nil
}

// releaseAll frees everything a leaving session holds, with the one release event. It runs
// inside the leave's write, so nothing slips between the leave and the release, and it deletes
// by holder directly: the sweep would already have taken the rows, since the session reads as
// left by now. A leave never fails on its reservations.
func (c *Channel) releaseAll(ctx context.Context, name string) {
	var paths []string
	err := c.serial(ctx, func(ctx context.Context) error {
		return c.tx(ctx, func(ctx context.Context, q store.Q) error {
			rows, err := q.All(ctx, "SELECT path FROM reservations WHERE holder=?", name)
			if err != nil {
				return err
			}
			for _, r := range rows {
				paths = append(paths, rStr(r, "path"))
			}
			if len(paths) == 0 {
				return nil
			}
			return q.Run(ctx, "DELETE FROM reservations WHERE holder=?", name)
		})
	})
	if err != nil || len(paths) == 0 {
		return
	}
	msg := utf16Cut(fmt.Sprintf("%s released %s", name, strings.Join(paths, ", ")), 300)
	_, _ = c.append(ctx, name, evIn{topic: "reserve.release", msg: sp(msg), data: reserveData(paths)})
}

// Reservations lists the reservations that hold right now, newest first; holder keeps one
// session's ("" is every session's).
func Reservations(ctx context.Context, c *Channel, holder string) (ReservationsView, error) {
	var out ReservationsView
	err := c.serial(ctx, func(ctx context.Context) error {
		return c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := sweepTx(ctx, q, c.nowISO()); err != nil {
				return err
			}
			rows, err := liveRows(ctx, q, holder)
			if err != nil {
				return err
			}
			out = ReservationsView{Reservations: []ReservationView{}}
			for _, r := range rows {
				out.Reservations = append(out.Reservations, ReservationView{
					Path: rStr(r, "path"), Holder: rStr(r, "holder"), Note: rStrPtr(r, "note"),
					Created: rStr(r, "created"), Expires: rStr(r, "expires"),
				})
			}
			return nil
		})
	})
	return out, err
}

// Reserved answers who holds one file the caller is about to change: the path itself, a
// directory reservation above it, or — when both sides report the same repository identity —
// the same path inside that repository. The caller's own reservations are silent.
func Reserved(ctx context.Context, c *Channel, me string, p, repo, rel string) (ReservedResult, error) {
	clean, err := cleanReservePath(p)
	if err != nil {
		return ReservedResult{}, err
	}
	name := holderOf(me)
	var out ReservedResult
	err = c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, name); err != nil {
			return err
		}
		return c.tx(ctx, func(ctx context.Context, q store.Q) error {
			out = ReservedResult{Path: clean}
			if err := sweepTx(ctx, q, c.nowISO()); err != nil {
				return err
			}
			rows, err := q.All(ctx, "SELECT path, repo, rel, holder, note, expires FROM reservations")
			if err != nil {
				return err
			}
			for _, r := range rows {
				if rStr(r, "holder") == name {
					continue
				}
				if !coversPath(rStr(r, "path"), clean) && !coversRel(rStr(r, "repo"), rStr(r, "rel"), repo, rel) {
					continue
				}
				holder := rStr(r, "holder")
				until := rStr(r, "expires")
				note := rStrPtr(r, "note")
				out = ReservedResult{
					Held: true, Path: clean, Holder: &holder, Until: &until, Note: note,
				}
				return nil
			}
			return nil
		})
	})
	return out, err
}

// coversPath answers whether the cleaned absolute path p is the reserved path or lies under it.
func coversPath(reserved, p string) bool {
	return reserved == p || strings.HasPrefix(p, reserved+"/")
}

// coversRel answers the same over two paths inside one repository: the match only counts when
// both sides reported the same repository identity.
func coversRel(reservedRepo, reservedRel, repo, rel string) bool {
	if reservedRepo == "" || reservedRel == "" || repo == "" || rel == "" || reservedRepo != repo {
		return false
	}
	return reservedRel == rel || strings.HasPrefix(rel, reservedRel+"/")
}

// reserveText renders one reserve's answer, one line per path.
func reserveText(r any, _ opCall) string {
	res, ok := r.(ReserveResult)
	if !ok {
		return ""
	}
	lines := []string{}
	for _, p := range res.Paths {
		if p.Held {
			lines = append(lines, fmt.Sprintf("%s held", p.Path))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s taken by %s until %s", p.Path, deref(p.Holder), deref(p.Until)))
	}
	return strings.Join(lines, "\n")
}

// releaseText renders one release's answer.
func releaseText(r any, _ opCall) string {
	res, ok := r.(ReleaseResult)
	if !ok || len(res.Released) == 0 {
		return "nothing released"
	}
	return "released " + strings.Join(res.Released, ", ")
}

// reservationsText renders the list the way an agent reads it.
func reservationsText(r any, _ opCall) string {
	res, ok := r.(ReservationsView)
	if !ok || len(res.Reservations) == 0 {
		return "(no reservations)"
	}
	lines := []string{}
	for _, v := range res.Reservations {
		line := fmt.Sprintf("%s — %s until %s", v.Path, v.Holder, v.Expires)
		if deref(v.Note) != "" {
			line += fmt.Sprintf(" (%s)", deref(v.Note))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// reservedText renders the PreToolUse warning: the one line the editing session sees. A path
// nobody holds renders nothing.
func reservedText(r any, _ opCall) string {
	res, ok := r.(ReservedResult)
	if !ok || !res.Held {
		return ""
	}
	holder := deref(res.Holder)
	note := ""
	if deref(res.Note) != "" {
		note = fmt.Sprintf(" (%s)", deref(res.Note))
	}
	return fmt.Sprintf("%s is reserved by %s until %s%s. Coordinate with them (huddle send %s …) before changing it.",
		res.Path, holder, deref(res.Until), note, holder)
}
