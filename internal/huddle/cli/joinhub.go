// `huddle join-hub <url> <invite>`: a machine spends the one-use invite
// `huddle pair --satellite` minted on the hub, keeps the credential that comes back under the
// home's keys, and writes its role and hub address into config.toml. A restart takes the role
// up: the server it starts then links to the hub.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// JoinHub joins this machine to its hub as a satellite.
func JoinHub(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "join-hub" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if len(pos) != 2 {
		fmt.Fprintln(stderr, "usage: huddle join-hub <url> <invite> [--name <machine>]")
		return 2
	}
	url, invite := strings.TrimRight(pos[0], "/"), pos[1]
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		fmt.Fprintln(stderr, "huddle: the hub's url starts https:// (or http:// for a loopback hub)")
		return 2
	}
	if !auth.SatelliteRe.MatchString(invite) {
		fmt.Fprintln(stderr, "huddle: the invite is the credential `huddle pair --satellite` printed")
		return 2
	}
	h := home.Home{Root: root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	name := cfg.Machine
	if v, ok := opt.text("name"); ok && v != "" {
		name = v
	}
	if !auth.DeviceNameRe.MatchString(name) {
		fmt.Fprintf(stderr, "huddle: --name %s: a letter or digit first, up to 32 of them, spaces and dashes\n", name)
		return 2
	}
	credential, err := exchangeInvite(ctx, url, invite, name)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 2
	}
	if err := home.SaveSatelliteSecret(ctx, w.secrets(ctx, h), url, credential); err != nil {
		fmt.Fprintf(stderr, "huddle: could not store the credential: %v\n", err)
		return 1
	}
	cfg.Role = "satellite"
	cfg.Hub.URL = url
	cfg.Machine = name
	if err := home.SaveConfig(h, cfg); err != nil {
		fmt.Fprintf(stderr, "huddle: could not write config.toml: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "joined %s as %s\n\n  The credential sits in the OS keystore, or this home's keys where there is none;\n  a restart takes the role up:\n\n    huddle up\n\n  This machine's sessions then appear on the hub's Now list, and its\n  permission prompts are answered from there.\n", url, name)
	return 0
}

// exchangeInvite spends one invite at the hub and answers the credential it minted.
func exchangeInvite(ctx context.Context, url, invite, name string) (string, error) {
	body, err := json.Marshal(map[string]string{"invite": invite, "name": name})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/sat/join", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-huddle-action", "/api/sat/join")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return "", fmt.Errorf("the hub at %s does not answer: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	var answer struct {
		Credential string `json:"credential"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", fmt.Errorf("the hub answered %s", response.Status)
	}
	if response.StatusCode != http.StatusOK || answer.Credential == "" {
		if answer.Error != "" {
			return "", errors.New(answer.Error)
		}
		return "", fmt.Errorf("the hub answered %s", response.Status)
	}
	return answer.Credential, nil
}
