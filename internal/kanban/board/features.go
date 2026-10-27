package board

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ClosedFeature is one feature a board closed: who closed it and when. Closing
// moves no card; it only records the name here.
type ClosedFeature struct {
	Name     string `json:"name"`
	Actor    string `json:"actor"`
	ClosedAt string `json:"closed_at"`
}

// featuresFile is the FEATURES record: the closed features, sorted by name.
type featuresFile struct {
	Closed []ClosedFeature `json:"closed"`
}

// ClosedFeatures answers the board's closed features sorted by name. A missing
// or unreadable FEATURES reads as none, the way stopped treats STOP-LINE.
func (b Board) ClosedFeatures() []ClosedFeature {
	data, err := b.fs().readFile(b.featuresPath())
	if err != nil {
		return nil
	}
	var rec featuresFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	slices.SortFunc(rec.Closed, func(x, y ClosedFeature) int { return strings.Compare(x.Name, y.Name) })
	return rec.Closed
}

// CloseFeature records a merged feature as closed: its cards stay where they
// are, and later cycles leave them out. A feature no card carries is refused; a
// second close answers the already-closed line and writes nothing.
func (b Board) CloseFeature(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.closeFeature(ctx, a) })
}

func (b Board) closeFeature(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(a.ID)
	if name == "" {
		return nil, refused("feature: name required")
	}
	n, err := b.featureCards(ctx, name)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, refused("no feature %s on this board", name)
	}
	closed := b.ClosedFeatures()
	if slices.ContainsFunc(closed, func(f ClosedFeature) bool { return f.Name == name }) {
		return []string{fmt.Sprintf("feature %s is already closed", name)}, nil
	}
	closed = append(closed, ClosedFeature{Name: name, Actor: a.Actor, ClosedAt: b.stamp()})
	if err := b.writeFeatures(ctx, closed); err != nil {
		return nil, err
	}
	if err := b.appendLog("feature-close", logStr("feature", name), logStr("actor", a.Actor)); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("feature %s closed: %d cards archived", name, n)}, nil
}

// ReopenFeature clears a feature's closed entry: its cards read as they did.
func (b Board) ReopenFeature(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.reopenFeature(ctx, a) })
}

func (b Board) reopenFeature(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(a.ID)
	closed := b.ClosedFeatures()
	kept := slices.DeleteFunc(slices.Clone(closed), func(f ClosedFeature) bool { return f.Name == name })
	if len(kept) == len(closed) {
		return nil, refused("feature %s is not closed", name)
	}
	n, err := b.featureCards(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		if err := b.fs().remove(b.featuresPath()); err != nil {
			return nil, err
		}
	} else if err := b.writeFeatures(ctx, kept); err != nil {
		return nil, err
	}
	if err := b.appendLog("feature-reopen", logStr("feature", name), logStr("actor", a.Actor)); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("feature %s reopened: %d cards back", name, n)}, nil
}

// featureCards counts the board's cards that carry the feature as their cluster.
func (b Board) featureCards(ctx context.Context, name string) (int, error) {
	cards, err := b.List(ctx, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cards {
		if c.Cluster == name {
			n++
		}
	}
	return n, nil
}

// writeFeatures lands the FEATURES record whole: JSON, sorted by name, one
// trailing newline.
func (b Board) writeFeatures(ctx context.Context, closed []ClosedFeature) error {
	slices.SortFunc(closed, func(x, y ClosedFeature) int { return strings.Compare(x.Name, y.Name) })
	data, err := json.Marshal(featuresFile{Closed: closed})
	if err != nil {
		return err
	}
	return b.fs().writeFile(ctx, b.featuresPath(), append(data, '\n'))
}
