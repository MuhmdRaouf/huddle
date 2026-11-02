package server

// The artifacts route: the pages one session's main transcript published, from its Artifact
// calls.

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

var sessionArtifactsPath = regexp.MustCompile(`^/api/sessions/([^/]+)/artifacts$`)

// artifactsRoute answers one session's artifacts in first-publish order. A session whose reports
// never named a transcript, and one whose transcript the disk lost, answer none.
func (s *server) artifactsRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	transcript, err := s.sessionTranscriptOf(r, sessionArtifactsPath.FindStringSubmatch(path)[1])
	if err != nil {
		return err
	}
	artifacts := []convo.Artifact{}
	if transcript != "" {
		listed, err := readArtifacts(transcript)
		if err != nil {
			return err
		}
		artifacts = listed
	}
	s.writeJSON(w, 200, struct {
		Artifacts []convo.Artifact `json:"artifacts"`
	}{artifacts}, nil)
	return nil
}

// readArtifacts answers one transcript's artifacts; a missing file lists none.
func readArtifacts(transcript string) ([]convo.Artifact, error) {
	file, err := os.Open(transcript)
	if errors.Is(err, fs.ErrNotExist) {
		return []convo.Artifact{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return convo.Artifacts(file)
}
