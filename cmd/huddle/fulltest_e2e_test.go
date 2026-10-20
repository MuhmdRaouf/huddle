package main

// The full scenario on a local dev name: one TLS hub named hub.example.invalid serving an imported
// pair, two satellites joining over invitations, their sessions synced through the hooks, the
// two sessions tied to one channel reading each other's digests once each, and radar answering
// the seeded fixtures' exact counts, tokens and cost. A second hub home with no import proves
// the local CA's leaf. The name resolves nowhere: this test's clients map it onto loopback in
// their dialer, the child processes reach it through a CONNECT proxy that maps it the same way,
// and no /etc/hosts line is ever touched. Every process is a child of this test and every home
// a temp one; nothing of the real machine is reached.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// fullHubName is the name everything here serves and dials: a reserved name no resolver answers,
// so the children reach the hub only through the proxy.
const fullHubName = "hub.example.invalid"

// fullChannel is the one channel both satellite sessions are tied to.
const fullChannel = "fulltest"

// fullNowPlaceholder is the timestamp placeholder the radar fixtures carry, offset in
// milliseconds from the run's own clock.
var fullNowPlaceholder = regexp.MustCompile(`@@NOW([+-]\d+)?@@`)

// TestFullScenarioOnALocalDevName walks the whole leg one step at a time; a step that fails
// stops the ones after it, because each builds on the world the earlier ones made.
func TestFullScenarioOnALocalDevName(t *testing.T) {
	parent := t
	root := t.TempDir()
	// registered first so it runs last: every server's own cleanup has stopped its child
	// before this proves none of them survived
	t.Cleanup(func() { noProcessHolds(t, root) })

	pluginCopy := filepath.Join(root, "plugin")
	binHome := filepath.Join(root, "huddle-bin")
	hubRoot := filepath.Join(root, "hub-home")
	hubUser := filepath.Join(root, "hub-user")
	hubProject := filepath.Join(root, "hub-project")
	certDir := filepath.Join(root, "certs")
	satARoot := filepath.Join(root, "sat-a-home")
	satAUser := filepath.Join(root, "sat-a-user")
	satABin := filepath.Join(root, "sat-a-bin")
	satAProject := filepath.Join(root, "sat-a-project")
	satBRoot := filepath.Join(root, "sat-b-home")
	satBUser := filepath.Join(root, "sat-b-user")
	satBBin := filepath.Join(root, "sat-b-bin")
	satBProject := filepath.Join(root, "sat-b-project")
	hub2Root := filepath.Join(root, "hub2-home")
	hub2User := filepath.Join(root, "hub2-user")
	for _, dir := range []string{
		filepath.Join(hubProject, ".git", "info"),
		filepath.Join(satAProject, ".git", "info"),
		filepath.Join(satBProject, ".git", "info"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make the project: %v", err)
		}
	}
	// each satellite's session names the shared channel, so its Stop hook digests and its
	// prompts take what the channel owes it
	for _, project := range []struct{ dir, as string }{
		{satAProject, "a"}, {satBProject, "b"},
	} {
		dir := filepath.Join(project.dir, ".agents", "huddle")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make the project's settings dir: %v", err)
		}
		body := `{"channel": "` + fullChannel + `", "as": "` + project.as + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "huddle.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write the project's settings: %v", err)
		}
	}

	var (
		release   *httptest.Server
		hubBin    string
		tlsPort   int
		caPEM     []byte
		satExtras map[string]string
		hubTLS    *http.Client
		inviteURL string
		inviteA   string
		credA     string
		credB     string
		hubLog    *serveLog
		wantRadar fixtureTotals
	)
	hubBase := func() string { return "https://" + fullHubName + ":" + strconv.Itoa(tlsPort) }
	satBinOf := func(binHome string) string {
		return filepath.Join(binHome, huddleVersion(t), "huddle")
	}
	// registryLists answers whether the hub's Now list names one session under one machine,
	// read over TLS the way a satellite's credential reads it
	registryLists := func(session, machine string) bool {
		code, body := tlsFetch(t, hubTLS, hubBase(), "/api/registry/sessions", "Bearer "+credA)
		if code != 200 {
			return false
		}
		var list struct {
			Sessions []struct {
				SessionID string `json:"session_id"`
				Machine   string `json:"machine"`
			} `json:"sessions"`
		}
		if json.Unmarshal([]byte(body), &list) != nil {
			return false
		}
		for _, row := range list.Sessions {
			if row.SessionID == session && row.Machine == machine {
				return true
			}
		}
		return false
	}
	// promptOf runs one UserPromptSubmit the way a session's next prompt does and answers what
	// its hooks printed
	promptOf := func(t *testing.T, env map[string]string, dir, session string) string {
		out := ""
		for _, command := range hookCommands(t, pluginCopy, "UserPromptSubmit") {
			code, stdout, stderr := runSh(t, 30*time.Second, env, dir, command,
				`{"hook_event_name":"UserPromptSubmit","session_id":"`+session+
					`","prompt":"go on","cwd":`+strconv.Quote(dir)+`}`)
			if code != 0 {
				t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
			}
			out = stdout
		}
		return out
	}
	// stopOf runs one Stop hook over a transcript, the moment a session's turn ends and its
	// digest rides the report
	stopOf := func(t *testing.T, env map[string]string, dir, session, transcript string) {
		for _, command := range hookCommands(t, pluginCopy, "Stop") {
			code, stdout, stderr := runSh(t, 30*time.Second, env, dir, command,
				`{"hook_event_name":"Stop","session_id":"`+session+
					`","transcript_path":`+strconv.Quote(transcript)+
					`,"cwd":`+strconv.Quote(dir)+`}`)
			if code != 0 {
				t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
			}
		}
	}
	hookEnvOf := func(root, user, binHome, project, loop, token string) map[string]string {
		return hookEnv(root, user, binHome, pluginCopy, project, "http://127.0.0.1:"+loop, token, nil)
	}
	satEnvOf := func(t *testing.T, machine satMachine) map[string]string {
		return hookEnvOf(machine.root, machine.user, machine.bin, machine.project,
			strconv.Itoa(savedLoopOf(t, machine.root)), machine.name+"-root-token")
	}
	// satelliteJoin spends one invite line on one machine, starts its server, waits for its
	// link and reports one session through its hooks; it answers the machine's credential
	satLogs := map[string]*serveLog{}
	satelliteJoin := func(t *testing.T, machine satMachine, inviteLine string) string {
		env := machineEnv(machine.root, machine.user, machine.bin, pluginCopy, machine.project,
			release.URL, satExtras)
		code, stdout, stderr := runSh(t, 90*time.Second, env, machine.project,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" join-hub `+inviteLine+` --name `+machine.name, "")
		if code != 0 {
			t.Fatalf("huddle join-hub = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "joined "+inviteURL+" as "+machine.name) {
			t.Fatalf("huddle join-hub said %q, want the join named", stdout)
		}
		credential, err := home.LoadSatelliteSecret(context.Background(),
			home.KeyFiles{Dir: home.Home{Root: machine.root}.Keys()}, inviteURL)
		if err != nil {
			t.Fatalf("read %s's credential: %v", machine.name, err)
		}
		log := startServe(parent, t, satBinOf(machine.bin), serveEnv{
			home: machine.root, user: machine.user, port: pinFreePort(t, machine.root),
			token: machine.name + "-root-token", data: filepath.Join(machine.user, "data"),
			extra: satExtras,
		})
		satLogs[machine.name] = log
		until(t, 30*time.Second, func() bool {
			return strings.Contains(log.String(), "satellite link to "+inviteURL+" as "+machine.name)
		}, machine.name+"'s link to the hub to come up")
		for _, command := range hookCommands(t, pluginCopy, "SessionStart") {
			code, stdout, stderr := runSh(t, 30*time.Second, satEnvOf(t, machine), machine.project,
				command, `{"hook_event_name":"SessionStart","session_id":"s-full-`+machine.name+
					`","source":"startup","cwd":`+strconv.Quote(machine.project)+`}`)
			if code != 0 {
				t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
			}
		}
		return credential
	}
	machineA := satMachine{name: "sat-a", root: satARoot, user: satAUser, bin: satABin, project: satAProject}
	machineB := satMachine{name: "sat-b", root: satBRoot, user: satBUser, bin: satBBin, project: satBProject}

	step := func(name string, body func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			if parent.Failed() {
				t.Fatalf("an earlier step failed; this one cannot run")
			}
			body(t)
		})
	}

	step("tls_pair", func(t *testing.T) {
		caPEM = fullTestPair(t, certDir, fullHubName)
	})

	step("install", func(t *testing.T) {
		if err := os.CopyFS(pluginCopy, os.DirFS(pluginSource(t))); err != nil {
			t.Fatalf("copy the plugin: %v", err)
		}
		release = fakeReleaseServer(parent, t, huddleBin)
		pinLauncherChecksum(t, filepath.Join(pluginCopy, "dist", "run"), huddleBin)
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 90*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" help`, "")
		if code != 0 {
			t.Fatalf("dist/run help = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		hubBin = filepath.Join(binHome, huddleVersion(t), "huddle")
		info, err := os.Stat(hubBin)
		if err != nil || info.Mode()&0o100 == 0 {
			t.Fatalf("the downloaded binary at %s is missing or not executable: %v", hubBin, err)
		}
	})

	step("hub_role_and_import", func(t *testing.T) {
		tlsPort = freeLoopPort(t)
		listen := "127.0.0.1:" + strconv.Itoa(tlsPort)
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" role hub --listen `+listen+` --host `+fullHubName, "")
		if code != 0 {
			t.Fatalf("huddle role hub = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "role hub: paired devices and satellites answer on "+listen) {
			t.Fatalf("huddle role hub said %q, want the listener named", stdout)
		}
		code, stdout, stderr = runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" cert --import "`+
				filepath.Join(certDir, "cert.pem")+`" "`+filepath.Join(certDir, "key.pem")+`"`, "")
		if code != 0 {
			t.Fatalf("huddle cert --import = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "certificate for "+fullHubName) {
			t.Fatalf("huddle cert --import said %q, want the name certified", stdout)
		}
	})

	step("hub_up", func(t *testing.T) {
		wantRadar = seedRadarFixtures(t, filepath.Join(hubUser, ".claude"),
			filepath.Join(hubUser, "radar"), "s-full-sat-a", satAProject)
		proxy := connectProxy(parent, t, "127.0.0.1:"+strconv.Itoa(tlsPort))
		if err := os.MkdirAll(filepath.Join(certDir, "empty"), 0o700); err != nil {
			t.Fatalf("make the empty cert dir: %v", err)
		}
		// the children reach the hub's name through the proxy and trust nothing but the
		// test's CA: Go's own proxy and root-file variables, read at request time
		satExtras = map[string]string{
			"HTTPS_PROXY": proxy.URL, "https_proxy": proxy.URL,
			"SSL_CERT_FILE": filepath.Join(certDir, "ca.pem"),
			"SSL_CERT_DIR":  filepath.Join(certDir, "empty"),
		}
		hubTLS = mappedClient(t, caPEM, fullHubName, tlsPort)
		loop := pinFreePort(t, hubRoot)
		hubLog = startServe(parent, t, hubBin, serveEnv{
			home: hubRoot, user: hubUser, port: loop,
			token: "hub-root-token", data: filepath.Join(hubUser, "data"),
		})
		until(t, 30*time.Second, func() bool {
			return healthy("http://127.0.0.1:" + strconv.Itoa(loop))
		}, "the hub to answer /health on loopback")
		// the hub listener's public shell proves the imported pair serves TLS the test's CA
		// vouches for; everything else on that listener wants a credential the later steps hold
		lastErr := ""
		if !pollFor(30*time.Second, func() bool {
			code, body := tlsFetch(t, hubTLS, hubBase(), "/", "")
			if code != 200 {
				lastErr = body
				return false
			}
			return true
		}) {
			t.Logf("the last TLS answer was %q", lastErr)
			t.Logf("=== the hub serve log ===\n%s", hubLog.String())
			t.Fatalf("the hub never served its shell over TLS as %s", fullHubName)
		}
	})

	step("invite_a", func(t *testing.T) {
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" pair --satellite --name sat-a`, "")
		if code != 0 {
			t.Fatalf("huddle pair --satellite = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		match := regexpSatelliteJoin.FindStringSubmatch(stdout)
		if match == nil {
			t.Fatalf("no join-hub line in the pair output: %q", stdout)
		}
		if match[1] != hubBase() {
			t.Fatalf("the join line names %s, want %s", match[1], hubBase())
		}
		inviteURL, inviteA = match[1], match[1]+" "+match[2]
	})

	step("join_a", func(t *testing.T) {
		credA = satelliteJoin(t, machineA, inviteA)
	})

	step("invite_b_join_b", func(t *testing.T) {
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" pair --satellite --name sat-b`, "")
		if code != 0 {
			t.Fatalf("huddle pair --satellite = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		match := regexpSatelliteJoin.FindStringSubmatch(stdout)
		if match == nil || match[1] != hubBase() {
			t.Fatalf("no join-hub line for satellite B in %q", stdout)
		}
		credB = satelliteJoin(t, machineB, match[1]+" "+match[2])
	})

	step("invitations_are_one_use", func(t *testing.T) {
		env := machineEnv(machineB.root, machineB.user, machineB.bin, pluginCopy,
			machineB.project, release.URL, satExtras)
		code, _, _ := runSh(t, 60*time.Second, env, machineB.project,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" join-hub `+inviteA+` --name sat-b`, "")
		if code == 0 {
			t.Fatal("satellite A's spent invite joined a second machine, want a refusal")
		}
		madeUp := inviteURL + " hsat_madeupinvite0123456789abcdef --name sat-b"
		code, _, _ = runSh(t, 60*time.Second, env, machineB.project,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" join-hub `+madeUp, "")
		if code == 0 {
			t.Fatal("a made-up invite joined the hub, want a refusal")
		}
	})

	step("now_lists_both", func(t *testing.T) {
		for _, machine := range []satMachine{machineA, machineB} {
			if pollFor(40*time.Second, func() bool { return registryLists("s-full-"+machine.name, machine.name) }) {
				continue
			}
			dumpChildDiagnostics(t, machine.root, machine.bin, machine.user, machine.project)
			t.Logf("=== %s's serve log ===\n%s", machine.name, satLogs[machine.name])
			t.Logf("=== %s's registry now === %s", machine.name, registryNow(t,
				"http://127.0.0.1:"+strconv.Itoa(savedLoopOf(t, machine.root)), machine.name+"-root-token"))
			t.Fatalf("timed out waiting for the hub's Now list to name s-full-%s under %s", machine.name, machine.name)
		}
	})

	step("one_channel", func(t *testing.T) {
		// the tie is the owner's reach into sessions, so it is written on the hub machine
		// itself: over loopback with the root token, where nothing owes a passkey
		code, body := loopbackCall(t, "http://127.0.0.1:"+strconv.Itoa(savedLoopOf(t, hubRoot)),
			"hub-root-token", http.MethodPost,
			"/api/links", `{"sessions":["s-full-sat-a","s-full-sat-b"],"channel":"`+
				fullChannel+`","create":true,"level":"digest"}`)
		if code != 200 {
			t.Fatalf("the link = %d: %s", code, body)
		}
		// the channel's presence over TLS: both sessions tied to it, read with a satellite's
		// credential the hub's gate accepts
		code, body = tlsFetch(t, hubTLS, hubBase(), "/api/links/"+fullChannel, "Bearer "+credB)
		if code != 200 {
			t.Fatalf("the channel's links = %d: %s", code, body)
		}
		for _, session := range []string{"s-full-sat-a", "s-full-sat-b"} {
			if !strings.Contains(body, `"`+session+`"`) {
				t.Fatalf("the channel's links = %s, want %s listed", body, session)
			}
		}
		// each session hears of the tie on its own next prompt, once
		for _, machine := range []satMachine{machineA, machineB} {
			env := satEnvOf(t, machine)
			until(t, 20*time.Second, func() bool {
				return strings.Contains(promptOf(t, env, machine.project, "s-full-"+machine.name),
					"linked into #"+fullChannel)
			}, machine.name+"'s session to hear of the tie")
		}
	})

	step("a_posts_b_reads_once", func(t *testing.T) {
		said := "sat a closed the alpha step and hands the beta step over"
		transcript := filepath.Join(machineA.project, "digest-a.jsonl")
		writeDigestTranscript(t, transcript, "s-full-sat-a", said)
		stopOf(t, satEnvOf(t, machineA), machineA.project, "s-full-sat-a", transcript)

		envB := satEnvOf(t, machineB)
		until(t, 30*time.Second, func() bool {
			return strings.Contains(promptOf(t, envB, machineB.project, "s-full-sat-b"), "alpha step")
		}, "satellite B's session to read A's digest")
		if again := promptOf(t, envB, machineB.project, "s-full-sat-b"); strings.Contains(again, "alpha step") {
			t.Fatalf("satellite B read A's digest twice: %q", again)
		}
	})

	step("b_posts_a_reads_once", func(t *testing.T) {
		said := "sat b closed the beta step and files the gamma notes"
		transcript := filepath.Join(machineB.project, "digest-b.jsonl")
		writeDigestTranscript(t, transcript, "s-full-sat-b", said)
		stopOf(t, satEnvOf(t, machineB), machineB.project, "s-full-sat-b", transcript)

		envA := satEnvOf(t, machineA)
		until(t, 30*time.Second, func() bool {
			return strings.Contains(promptOf(t, envA, machineA.project, "s-full-sat-a"), "beta step")
		}, "satellite A's session to read B's digest")
		if again := promptOf(t, envA, machineA.project, "s-full-sat-a"); strings.Contains(again, "beta step") {
			t.Fatalf("satellite A read B's digest twice: %q", again)
		}
	})

	step("radar_numbers", func(t *testing.T) {
		if wantRadar.requests != 3 {
			t.Fatalf("the fixture's today lines are %d, want the three the scenario promises", wantRadar.requests)
		}
		now := time.Now()
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		path := "/api/radar/api/requests?session=s-full-sat-a&since=" +
			strconv.FormatInt(midnight.UnixMilli(), 10)
		code, body := tlsFetch(t, hubTLS, hubBase(), path, "Bearer "+credA)
		if code != 200 {
			t.Fatalf("the radar requests = %d: %s", code, body)
		}
		var answer struct {
			Requests []struct {
				Tokens shared.Tokens `json:"tokens"`
			} `json:"requests"`
		}
		if err := json.Unmarshal([]byte(body), &answer); err != nil {
			t.Fatalf("the radar requests body %s: %v", body, err)
		}
		if int64(len(answer.Requests)) != wantRadar.requests {
			t.Fatalf("radar answered %d requests for s-full-sat-a today, want %d",
				len(answer.Requests), wantRadar.requests)
		}
		var got shared.Tokens
		for _, request := range answer.Requests {
			got.Input += request.Tokens.Input
			got.Output += request.Tokens.Output
			got.CacheRead += request.Tokens.CacheRead
			got.CacheWrite += request.Tokens.CacheWrite
		}
		if got.Input != wantRadar.input || got.Output != wantRadar.output ||
			got.CacheRead != wantRadar.cacheRead || got.CacheWrite != wantRadar.cacheWrite {
			t.Fatalf("radar's tokens = input %d output %d cache-read %d cache-write %d, want %d %d %d %d",
				got.Input, got.Output, got.CacheRead, got.CacheWrite,
				wantRadar.input, wantRadar.output, wantRadar.cacheRead, wantRadar.cacheWrite)
		}

		code, body = tlsFetch(t, hubTLS, hubBase(), "/api/radar/api/usage?days=7", "Bearer "+credA)
		if code != 200 {
			t.Fatalf("the usage report = %d: %s", code, body)
		}
		var report struct {
			Days []struct {
				Date string `json:"date"`
				usageTotals
			} `json:"days"`
		}
		if err := json.Unmarshal([]byte(body), &report); err != nil {
			t.Fatalf("the usage body %s: %v", body, err)
		}
		today := now.Format("2006-01-02")
		for _, day := range report.Days {
			if day.Date != today {
				continue
			}
			if day.Requests != wantRadar.requests || day.Input != wantRadar.input ||
				day.Output != wantRadar.output || day.CacheRead != wantRadar.cacheRead ||
				day.CacheWrite != wantRadar.cacheWrite {
				t.Fatalf("today's usage = %+v, want the fixture's sums %+v", day.usageTotals, wantRadar)
			}
			if cents(day.CostUSD) != cents(wantRadar.costUSD) {
				t.Fatalf("today's usage cost = %.4f, want %.4f", day.CostUSD, wantRadar.costUSD)
			}
			// the spend the hub's own cards read is the same money
			until(t, 20*time.Second, func() bool {
				code, body := tlsFetch(t, hubTLS, hubBase(), "/api/radar/api/spend", "Bearer "+credA)
				if code != 200 {
					return false
				}
				var spend struct {
					TodayUsd *float64 `json:"todayUsd"`
				}
				if json.Unmarshal([]byte(body), &spend) != nil || spend.TodayUsd == nil {
					return false
				}
				return cents(*spend.TodayUsd) == cents(wantRadar.costUSD)
			}, "today's spend to equal the fixture's cost")
			return
		}
		t.Fatalf("the usage report carries no day %s: %s", today, body)
	})

	step("radar_writes", func(t *testing.T) {
		// the owner's phone pairs over TLS: the passkey ceremony is the one way the hub
		// listener admits a browser
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" pair --name fulltest-phone`, "")
		if code != 0 {
			t.Fatalf("huddle pair = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		link := regexp.MustCompile(regexp.QuoteMeta(hubBase()) + `/pair#(\S+)`).FindStringSubmatch(stdout)
		if link == nil {
			t.Fatalf("no pairing link for %s in the pair output: %q", hubBase(), stdout)
		}
		phone := webauthntest.NewSoftKey(t)
		browser := newTLSBrowser(t, caPEM, fullHubName, tlsPort)
		code, body := tlsBrowserSend(t, browser, http.MethodPost, "/api/pair/begin", "",
			fmt.Sprintf(`{"token":%q,"name":"fulltest-phone"}`, link[1]))
		if code != 200 {
			t.Fatalf("the pair begin = %d: %s", code, body)
		}
		options, _ := parseBegin(t, body)
		answer := phone.Create(t, options, hubBase())
		code, body = tlsBrowserSend(t, browser, http.MethodPost, "/api/pair/finish", "",
			fmt.Sprintf(`{"token":%q,"response":%s}`, link[1], answer))
		if code != 200 {
			t.Fatalf("the pair finish = %d: %s", code, body)
		}
		// the TLS listener sets the session cookie under its __Host- name
		if cookie := browser.cookie(t, auth.PairHostCookie); cookie == "" {
			t.Fatal("the pairing over TLS earned no session cookie")
		}

		// the paired phone saves a budget through the Radar tab, its Origin the hub's own name
		budgets := `{"budgets":[{"id":"fulltest","scope":"total","period":"week","limitUsd":3,"action":"warn"}]}`
		code, body = tlsBrowserSend(t, browser, http.MethodPut, "/api/radar/api/budgets", hubBase(), budgets)
		if code != 200 {
			t.Fatalf("the phone's budget save = %d: %s", code, body)
		}
		// a foreign Origin never writes, here as anywhere else
		code, body = tlsBrowserSend(t, browser, http.MethodPut, "/api/radar/api/budgets",
			"https://evil.example", budgets)
		if code != 403 {
			t.Fatalf("the foreign origin's budget save = %d: %s, want 403", code, body)
		}
		// the save landed: the budgets the tab holds name it
		code, body = tlsFetch(t, hubTLS, hubBase(), "/api/radar/api/budgets", "Bearer "+credA)
		if code != 200 || !strings.Contains(body, `"fulltest"`) {
			t.Fatalf("the saved budgets = %d: %s, want the phone's budget listed", code, body)
		}
	})

	step("local_ca_hub", func(t *testing.T) {
		const secondName = "hub2.example.invalid"
		secondPort := freeLoopPort(t)
		env := machineEnv(hub2Root, hub2User, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" role hub --listen 127.0.0.1:`+
				strconv.Itoa(secondPort)+` --host `+secondName, "")
		if code != 0 {
			t.Fatalf("huddle role hub = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		startServe(parent, t, hubBin, serveEnv{
			home: hub2Root, user: hub2User, port: pinFreePort(t, hub2Root),
			token: "hub2-root-token", data: filepath.Join(hub2User, "data"),
		})
		rootFile := filepath.Join(hub2Root, "pki", "root.crt")
		until(t, 30*time.Second, func() bool {
			_, err := os.Stat(rootFile)
			return err == nil
		}, "the local CA to write its root")
		rootPEM, err := os.ReadFile(rootFile)
		if err != nil {
			t.Fatalf("read the local CA's root: %v", err)
		}
		second := mappedClient(t, rootPEM, secondName, secondPort)
		until(t, 30*time.Second, func() bool {
			code, _ := tlsFetch(t, second, "https://"+secondName+":"+strconv.Itoa(secondPort),
				"/", "")
			return code == 200
		}, "the second hub to serve its shell over a leaf only its own root vouches for")
	})
}

// satMachine is one satellite machine's places: its home, its user, its binary dir and project.
type satMachine struct {
	name    string
	root    string
	user    string
	bin     string
	project string
}

// usageTotals is one usage day's totals as the report spells them.
type usageTotals struct {
	Requests   int64   `json:"requests"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	CostUSD    float64 `json:"costUsd"`
}

// fixtureTotals is what radar must answer for the seeded fixtures: the request count, the four
// token sums and the cost, each computed from the fixture lines themselves.
type fixtureTotals struct {
	requests   int64
	input      int64
	output     int64
	cacheRead  int64
	cacheWrite int64
	costUSD    float64
}

// cents rounds one USD sum to its cents: money is compared to the cent, never past it.
func cents(usd float64) float64 { return math.Round(usd*100) / 100 }

// freeLoopPort answers one port the OS finds free on loopback right now.
func freeLoopPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// savedLoopOf answers the port pinFreePort saved in one home: the loopback port that home's
// server answers on.
func savedLoopOf(t *testing.T, root string) int {
	choice, has := cli.SavedPort(root)
	if !has {
		t.Fatalf("no saved port in %s", root)
	}
	return choice.Port
}

// fullTestPair mints one CA and one leaf naming host, writes the CA, the pair and an empty cert
// dir (the children's SSL_CERT_DIR, so nothing but the CA is trusted), and answers the CA's PEM.
func fullTestPair(t *testing.T, dir, host string) []byte {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("draw the CA's key: %v", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fulltest CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(90 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("mint the CA: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("draw the leaf's key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("mint the leaf: %v", err)
	}
	leafPKCS8, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal the leaf's key: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	writeFor := func(name string, body []byte, mode os.FileMode) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFor("ca.pem", caPEM, 0o644)
	writeFor("cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644)
	writeFor("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafPKCS8}), 0o600)
	return caPEM
}

// mappedClient answers one HTTP client whose dialer maps name:port onto loopback and whose TLS
// trusts nothing but the pool built from rootPEM: the name is served and verified for real,
// with no hosts file and no skipped verification.
func mappedClient(t *testing.T, rootPEM []byte, name string, port int) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		t.Fatal("the root PEM holds no certificate")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr == net.JoinHostPort(name, strconv.Itoa(port)) {
					addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				}
				return dialer.DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
}

// tlsFetch does one read over TLS, carrying the bearer a satellite's credential names.
func tlsFetch(t *testing.T, client *http.Client, base, path, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", path, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// newTLSBrowser answers the paired phone's browser over the hub's TLS listener: the same
// mapped dialer and root tlsFetch trusts, with a cookie jar the pairing fills.
func newTLSBrowser(t *testing.T, rootPEM []byte, name string, port int) *browserClient {
	t.Helper()
	client := mappedClient(t, rootPEM, name, port)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("make the cookie jar: %v", err)
	}
	client.Jar = jar
	return &browserClient{
		base:   "https://" + net.JoinHostPort(name, strconv.Itoa(port)),
		client: client,
	}
}

// tlsBrowserSend sends one request the phone's browser sends: JSON and the action header the
// guard demands on a write, the jar's cookies along, and the Origin a browser names on one —
// empty names none.
func tlsBrowserSend(t *testing.T, browser *browserClient, method, path, origin, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, browser.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set(server.ActionHeader, path)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := browser.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// loopbackCall does one credentialed call on the hub's loopback listener with the root token.
func loopbackCall(t *testing.T, base, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-action", path)
	req.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// connectProxy answers one proxy every CONNECT of which lands on target: the mapping the
// children's processes need, since they resolve no name and rewrite no hosts file.
func connectProxy(parent *testing.T, t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "the proxy answers CONNECT", http.StatusNotFound)
			return
		}
		inner, err := net.DialTimeout("tcp", target, 10*time.Second)
		if err != nil {
			http.Error(w, "the mapped address is unreachable", http.StatusBadGateway)
			return
		}
		defer inner.Close()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "the proxy cannot tunnel", http.StatusInternalServerError)
			return
		}
		client, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(inner, client)
			close(done)
		}()
		_, _ = io.Copy(client, inner)
		<-done
	}))
	parent.Cleanup(srv.Close)
	return srv
}

// noProcessHolds fails the leg when any process's command line still names the test's temp
// root: everything the leg started runs from under it, so one line left is one process left.
func noProcessHolds(t *testing.T, root string) {
	t.Helper()
	gone := func() bool {
		out, err := exec.Command("ps", "-axo", "command=").Output()
		if err != nil {
			t.Logf("cannot list the processes (%v); the leftover check is vacuous", err)
			return true
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, root) {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !gone() {
		time.Sleep(200 * time.Millisecond)
	}
	if !gone() {
		t.Errorf("a process still holds %s: the leg left something running", root)
	}
}

// writeDigestTranscript writes the one-turn transcript a Stop hook digests: the session's
// newest prompt is the whole digest.
func writeDigestTranscript(t *testing.T, path, session, said string) {
	t.Helper()
	line := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "user",
		"sessionId": session,
		"message":   map[string]any{"role": "user", "content": said},
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("shape the transcript line: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
}

// fixtureSpoolLine is one spool line's request facts; the web suite's fixtures carry the same
// fields the provider plugins' routers spool.
type fixtureSpoolLine struct {
	Ts       string `json:"ts"`
	Event    string `json:"event"`
	Model    string `json:"model"`
	Upstream string `json:"upstream"`
	Usage    *struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// seedRadarFixtures lands the web suite's fixtures under the hub machine's config and radar
// dirs — the transcript the watcher walks, the spool the router lines name — stamped with this
// run's clock and the satellite session's id, and answers the exact numbers radar must then
// report for today, priced the way the usage report prices its rows.
func seedRadarFixtures(t *testing.T, claudeConfig, radarHome, session, project string) fixtureTotals {
	t.Helper()
	fixtures := filepath.Join("..", "..", "web", "e2e", "fixtures")
	t0 := time.Now().Add(-time.Minute)
	stamp := func(raw string) string {
		named := strings.ReplaceAll(raw, "@@PROJECT@@", project)
		named = strings.ReplaceAll(named, "e2e-seed", session)
		return fullNowPlaceholder.ReplaceAllStringFunc(named, func(placeholder string) string {
			offset := int64(0)
			if len(placeholder) > 6 {
				if parsed, err := strconv.ParseInt(placeholder[5:len(placeholder)-2], 10, 64); err == nil {
					offset = parsed
				}
			}
			return t0.Add(time.Duration(offset) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		})
	}
	transcript, err := os.ReadFile(filepath.Join(fixtures, "transcript.jsonl"))
	if err != nil {
		t.Fatalf("read the transcript fixture: %v", err)
	}
	spool, err := os.ReadFile(filepath.Join(fixtures, "spool.jsonl"))
	if err != nil {
		t.Fatalf("read the spool fixture: %v", err)
	}
	transcriptDir := filepath.Join(claudeConfig, "projects", fullChannel)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("make the transcript dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, session+".jsonl"),
		[]byte(stamp(string(transcript))), 0o644); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
	spoolDir := filepath.Join(radarHome, "spool")
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		t.Fatalf("make the spool dir: %v", err)
	}
	stamped := stamp(string(spool))
	day := t0.UTC().Format("2006-01-02") + ".jsonl"
	if err := os.WriteFile(filepath.Join(spoolDir, day), []byte(stamped), 0o644); err != nil {
		t.Fatalf("write the spool: %v", err)
	}

	midnight := time.Date(t0.Year(), t0.Month(), t0.Day(), 0, 0, 0, 0, t0.Location())
	var want fixtureTotals
	for _, line := range strings.Split(stamped, "\n") {
		if !strings.Contains(line, `"event":"route"`) {
			continue
		}
		var parsed fixtureSpoolLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil || parsed.Usage == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, parsed.Ts)
		if err != nil {
			t.Fatalf("the stamped line's time %q: %v", parsed.Ts, err)
		}
		if at.Before(midnight) {
			continue
		}
		want.requests++
		want.input += parsed.Usage.Input
		want.output += parsed.Usage.Output
		want.cacheRead += parsed.Usage.CacheRead
		want.cacheWrite += parsed.Usage.CacheWrite
		// the same pricer the usage route reads, over the fixture's own line: the expected
		// cost is computed, never remembered
		price := cost.RequestCost(cost.PricedRequest{
			Model: parsed.Model, Upstream: parsed.Upstream, Ts: at.UnixMilli(),
			Tokens: shared.Tokens{
				Input: parsed.Usage.Input, Output: parsed.Usage.Output,
				CacheRead: parsed.Usage.CacheRead, CacheWrite: parsed.Usage.CacheWrite,
			},
		})
		if price == nil {
			t.Fatalf("the fixture's model %s has no price", parsed.Model)
		}
		want.costUSD += float64(price.USD)
	}
	if want.requests == 0 {
		t.Fatal("the fixtures carry no request inside today")
	}
	return want
}
