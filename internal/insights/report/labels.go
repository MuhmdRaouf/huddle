package report

import (
	"path/filepath"
	"strconv"
)

// LabelEntry is one row of the local label map: a short label and what it stands for.
type LabelEntry struct {
	Label string `json:"label"`
	Value string `json:"value"` // a repo root, or a session id
}

// Labels gives repo paths and session ids short names ("repo-1", "session-2") before anything
// leaves the machine. The map back to the real values stays in this process: it is never part of
// a Structure, and the run preview shows it so the owner sees what the labels stand for.
type Labels struct {
	repos    labelSet
	sessions labelSet
}

// NewLabels returns an empty map; labels are numbered in the order values are first seen.
func NewLabels() *Labels {
	return &Labels{
		repos:    labelSet{prefix: "repo-", byValue: map[string]string{}},
		sessions: labelSet{prefix: "session-", byValue: map[string]string{}},
	}
}

// Repo returns the label of a repo root; "" for an unknown repo. Paths are cleaned first, so
// "/work/a/" and "/work/a" share one label.
func (l *Labels) Repo(path string) string {
	if path == "" {
		return ""
	}
	return l.repos.label(filepath.Clean(path))
}

// Session returns the label of a session id.
func (l *Labels) Session(id string) string {
	if id == "" {
		return ""
	}
	return l.sessions.label(id)
}

// Repos lists the repo labels with their paths, in label order.
func (l *Labels) Repos() []LabelEntry { return l.repos.entries() }

// Sessions lists the session labels with their ids, in label order.
func (l *Labels) Sessions() []LabelEntry { return l.sessions.entries() }

type labelSet struct {
	prefix  string
	byValue map[string]string
	values  []string // in label order
}

func (s *labelSet) label(value string) string {
	if label, ok := s.byValue[value]; ok {
		return label
	}
	label := s.prefix + strconv.Itoa(len(s.values)+1)
	s.byValue[value] = label
	s.values = append(s.values, value)
	return label
}

func (s *labelSet) entries() []LabelEntry {
	out := make([]LabelEntry, len(s.values))
	for i, v := range s.values {
		out[i] = LabelEntry{Label: s.byValue[v], Value: v}
	}
	return out
}
