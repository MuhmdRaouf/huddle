package server

// The server's pieces that decide without a listener: the starts that refuse at once, the
// environment a start reads, the port walk's own corners, the role words' little helpers, the
// pid probe, the step-up that asks nothing and the push keys' own sources.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
)

func TestServeRefusesToStartWithoutItsDirectories(t *testing.T) {
	err := Serve(bg, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || err.Error() != "huddle: no data directory" {
		t.Errorf("Serve without a data dir = %v, want the refusal", err)
	}
	err = Serve(bg, Options{Data: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || err.Error() != "huddle: no home directory" {
		t.Errorf("Serve without a home = %v, want the refusal", err)
	}
}

func TestOptionsFromEnvReadsTheStartItFinds(t *testing.T) {
	// no environment function: the process's own variables answer
	o := OptionsFromEnv("1.2.3", nil, io.Discard, io.Discard, nil)
	if o.Version != "1.2.3" || o.Host == "" {
		t.Errorf("OptionsFromEnv with no env func = %+v, want a version and a host", o)
	}
	// HUDDLE_HOME unset: the home resolves from the environment it is given
	home := t.TempDir()
	env := map[string]string{"HUDDLE_HOME": home, "HOME": home}
	o = OptionsFromEnv("1.0.0", func(key string) string { return env[key] }, io.Discard, io.Discard, nil)
	if o.Home != home || o.Data != home {
		t.Errorf("OptionsFromEnv = home %q data %q, want the temp home for both", o.Home, o.Data)
	}
	// a root credential read from stdin, a sweep interval, and a project that dedups
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	env["HUDDLE_ROOT_STDIN"] = "1"
	env["HUDDLE_PERMIT_SWEEP_MS"] = "50"
	env["CLAUDE_PROJECT_DIR"] = wd
	o = OptionsFromEnv("1.0.0", func(key string) string { return env[key] }, io.Discard, io.Discard,
		strings.NewReader("the-root-token\n"))
	if o.Token != "the-root-token" || !o.RootStdin {
		t.Errorf("the root = %q (%v), want the line from stdin", o.Token, o.RootStdin)
	}
	if o.PermitSweepMS != 50 {
		t.Errorf("PermitSweepMS = %d, want 50", o.PermitSweepMS)
	}
	if projects := o.Projects(); len(projects) != 1 || projects[0] != wd {
		t.Errorf("Projects() = %v, want the working directory once", projects)
	}
	// a PORT that is not a number leaves the port unset but named
	env["PORT"] = "not-a-port"
	o = OptionsFromEnv("1.0.0", func(key string) string { return env[key] }, io.Discard, io.Discard, nil)
	if !o.PortNamed || o.Port != 0 {
		t.Errorf("a port that is not a number = (%d, %v), want named and unset", o.Port, o.PortNamed)
	}
}

// portAt is one address spelled as it bound, the shape hubListenHosts reads.
type portAt struct{ text string }

func (p portAt) Network() string { return "tcp" }
func (p portAt) String() string  { return p.text }

func TestHubListenHostsReadsTheSpellingsOneAddressLegitimises(t *testing.T) {
	bound := portAt{"192.168.1.7:8443"}
	hosts := hubListenHosts("0.0.0.0:8443", bound, "hub.example")
	want := []string{"0.0.0.0:8443", "0.0.0.0:8443", "192.168.1.7:8443", "hub.example", "hub.example:8443"}
	if strings.Join(hosts, "|") != strings.Join(want, "|") {
		t.Errorf("hubListenHosts = %v, want %v", hosts, want)
	}
	// an address without its port names no host, and neither does a bound form without one
	if got := hubListenHosts("hub.example", bound, ""); got != nil {
		t.Errorf("hubListenHosts of a listen without a port = %v, want none", got)
	}
	if got := hubListenHosts("0.0.0.0:8443", portAt{"just-a-host"}, ""); got != nil {
		t.Errorf("hubListenHosts of a bound form without a port = %v, want none", got)
	}
}

func TestHubPublicPathNamesWhatACredentialFreeBrowserNeeds(t *testing.T) {
	for _, path := range []string{"/", "/pair", "/app.js", "/static/app.css", "/fonts/x.woff2", "/icons/x.png", "/api/pair/begin", "/api/login/begin"} {
		if !hubPublicPath(path) {
			t.Errorf("hubPublicPath(%q) = false, want it public", path)
		}
	}
	for _, path := range []string{"/api/channels", "/api/needs", "/private"} {
		if hubPublicPath(path) {
			t.Errorf("hubPublicPath(%q) = true, want it behind the door", path)
		}
	}
}

func TestASatelliteWithoutARegistryIsNeverNamed(t *testing.T) {
	request, err := http.NewRequestWithContext(bg, http.MethodGet, "/api/channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer sat:"+strings.Repeat("a", 40))
	s := &server{}
	if machine, ok := s.satelliteBearer(request); ok || machine != "" {
		t.Errorf("satelliteBearer without a registry = (%q, %v), want none", machine, ok)
	}
	if _, ok := satelliteFrom(request.Context()); ok {
		t.Error("satelliteFrom of a plain request answered ok")
	}
	if fromHubListener(request.Context()) {
		t.Error("fromHubListener of a plain request answered true")
	}
}

func TestApplyRoleKeepsLoopbackWhenTheConfigCannotBeRead(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s := &server{o: Options{Home: home, Stdout: &out}}
	s.applyRole(bg, func(http.ResponseWriter, *http.Request) {})
	if out.Len() != 0 {
		t.Errorf("applyRole over an unreadable config printed %q, want silence", out.String())
	}
}

func TestApplyRoleOfAHubWithoutAListenSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name, config, want string
	}{
		{"no name", "role = \"hub\"\n", "role hub without hub.host"},
		{"behind a proxy", "role = \"hub\"\n\n[hub]\nhost = \"hub.example.invalid\"\n",
			"role hub as https://hub.example.invalid: loopback only, for a TLS reverse proxy on this machine to forward to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			s := &server{o: Options{Home: home, Stdout: &out}}
			s.applyRole(bg, func(http.ResponseWriter, *http.Request) {})
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("applyRole printed %q, want %q", out.String(), tc.want)
			}
		})
	}
}

func TestAPidThePsCanNameIsAliveAndOneItCannotIsGone(t *testing.T) {
	command, alive := pidCommandOf(bg, os.Getpid())
	if !alive || !strings.Contains(command, os.Args[0]) {
		t.Errorf("pidCommandOf(this process) = (%q, %v), want this binary", command, alive)
	}
	if _, alive := pidCommandOf(bg, 1<<30); alive {
		t.Error("pidCommandOf of a pid nothing owns answered alive")
	}
}

func TestTheSoloStepUpAsksNothing(t *testing.T) {
	request, _ := http.NewRequestWithContext(bg, http.MethodGet, "/", nil)
	if (soloStepUp{}).Required(request, auth.Who{}) {
		t.Error("the solo step-up asked for an assertion")
	}
}

func TestChoosePortTakesTheWalkWhenTheProbeSaysNothing(t *testing.T) {
	held := freePort(t)
	release := hold(t, held)
	defer release()
	// no probe at all: a held chosen port moves on, never hangs
	chosen, err := ChoosePort(bg, PortPrefs{Port: held, Auto: true}, nil)
	if err != nil {
		t.Fatalf("ChoosePort with no probe: %v", err)
	}
	if chosen.Port == held || chosen.Huddle {
		t.Errorf("chosen = %+v, want a walk past the held port", chosen)
	}
	// a cancelled context answers no to every probe, so nothing is reused
	cancelled, cancel := context.WithCancel(bg)
	cancel()
	chosen, err = ChoosePort(cancelled, PortPrefs{Port: held, Auto: true},
		func(context.Context, int) bool { return true })
	if err != nil {
		t.Fatalf("ChoosePort on a cancelled context: %v", err)
	}
	if chosen.Huddle {
		t.Errorf("chosen = %+v, want no huddle found on a dead context", chosen)
	}
}

func TestOursAnswersReadsOnlyAHealthyHuddleOfItsOwnHome(t *testing.T) {
	o := Options{Home: "/this/huddle"}
	// a service that answers 500 is nobody's
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(sick.Close)
	if oursAnswers(bg, o, portOf(sick.URL)) {
		t.Error("oursAnswers of a 500 answered true")
	}
	// a service that answers with text that is not JSON is nobody's either
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	t.Cleanup(garbled.Close)
	if oursAnswers(bg, o, portOf(garbled.URL)) {
		t.Error("oursAnswers of a non-JSON answer answered true")
	}
	// a healthy huddle of another home is still not ours
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"home":"/another/huddle"}`))
	}))
	t.Cleanup(other.Close)
	if oursAnswers(bg, o, portOf(other.URL)) {
		t.Error("oursAnswers of another home answered true")
	}
	ours := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"home":"/this/huddle"}`))
	}))
	t.Cleanup(ours.Close)
	if !oursAnswers(bg, o, portOf(ours.URL)) {
		t.Error("oursAnswers of our own home answered false")
	}
}

// portOf reads the port one httptest server answers on.
func portOf(rawURL string) int {
	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

func TestServeRefusesAChosenPortThatAnotherProgramHolds(t *testing.T) {
	home := t.TempDir()
	held := freePort(t)
	release := hold(t, held)
	defer release()
	config := "port = " + strconv.Itoa(held) + "\nport_auto = false\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Serve(bg, Options{Home: home, Data: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("Serve on a held chosen port answered nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("the refusal = %v, want it to name the port", err)
	}
}

func TestServeRefusesAChosenPortItCannotBind(t *testing.T) {
	held := freePort(t)
	release := hold(t, held)
	defer release()
	// no stdout or stderr of its own: the start falls back to the process's own
	err := Serve(bg, Options{Home: t.TempDir(), Data: t.TempDir(), Port: held, PortNamed: true})
	if err == nil {
		t.Fatal("Serve on a port it cannot bind answered nil, want the error")
	}
}

func TestVapidKeysComeFromTheFileAndFromTheKeystoreAlike(t *testing.T) {
	dir := t.TempDir()
	source := newVapidSource(nil, filepath.Join(dir, "run", "vapid.key"), nil)
	first, err := source.Keys(bg)
	if err != nil {
		t.Fatalf("the first keys: %v", err)
	}
	if first.Private == "" || first.Public == "" {
		t.Fatalf("the first keys = %+v, want both halves", first)
	}
	if info, err := os.Stat(filepath.Join(dir, "run", "vapid.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the key file is %v, want 0600", info)
	}
	if again, err := source.Keys(bg); err != nil || again != first {
		t.Errorf("the second keys = (%+v, %v), want the pair kept", again, err)
	}
	// a keystore that already holds a key answers with the same identity, and the file keeps it
	keyFile := filepath.Join(t.TempDir(), "vapid.key")
	same := newVapidSource(fakeVapidStore{key: first.Private, found: true}, keyFile, nil)
	fromStore, err := same.Keys(bg)
	if err != nil || fromStore != first {
		t.Errorf("the keystore's keys = (%+v, %v), want the file pair's identity", fromStore, err)
	}
	if raw, err := os.ReadFile(keyFile); err != nil || string(raw) != first.Private {
		t.Errorf("the key file = (%q, %v), want the keystore's key kept beside", raw, err)
	}
	// the keep is best effort: a file that cannot be written leaves the pair answering
	blockedFile := filepath.Join(t.TempDir(), "vapid.key")
	if err := os.MkdirAll(blockedFile, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := newVapidSource(fakeVapidStore{key: first.Private, found: true}, blockedFile, nil)
	if keys, err := blocked.Keys(bg); err != nil || keys != first {
		t.Errorf("the keys past an unwritable keep = (%+v, %v), want the keystore's pair", keys, err)
	}
	// a store whose value is junk, beside a file that cannot be written: the refusal surfaces
	hopelessFile := filepath.Join(t.TempDir(), "v2.key")
	if err := os.MkdirAll(hopelessFile, 0o700); err != nil {
		t.Fatal(err)
	}
	hopeless := newVapidSource(fakeVapidStore{key: "not base64 at all", found: true}, hopelessFile, nil)
	if _, err := hopeless.Keys(bg); err == nil {
		t.Error("Keys past a junk store and an unwritable file answered nil, want the write's error")
	}
}

// fakeVapidStore is a keystore that found a key, or none, or refuses to keep one.
type fakeVapidStore struct {
	key     string
	found   bool
	refuses bool
}

func (f fakeVapidStore) Get(context.Context) (string, bool) { return f.key, f.found }
func (f fakeVapidStore) Set(context.Context, string) error {
	if f.refuses {
		return errors.New("refused")
	}
	return nil
}

func TestVapidKeysFallBackWhenTheKeystoreHoldsNothing(t *testing.T) {
	// the store answers but holds no key: the fresh pair goes back to it, off to the side
	source := newVapidSource(fakeVapidStore{}, filepath.Join(t.TempDir(), "vapid.key"), nil)
	keys, err := source.Keys(bg)
	if err != nil || keys.Private == "" {
		t.Fatalf("the keys past an empty store = (%+v, %v), want a fresh pair", keys, err)
	}
	// the store's value is not a private key: the key file answers instead
	junk := fakeVapidStore{key: "not a key", found: true}
	file := filepath.Join(t.TempDir(), "vapid.key")
	paired := newVapidSource(junk, file, nil)
	first, err := paired.Keys(bg)
	if err != nil {
		t.Fatalf("the keys past a junk store: %v", err)
	}
	// the same pair reads back from the file alone
	alone := newVapidSource(nil, file, nil)
	if again, err := alone.Keys(bg); err != nil || again != first {
		t.Errorf("the file's keys = (%+v, %v), want the pair the first source made", again, err)
	}
	// a key file that holds no private key is remade, not obeyed
	stale := filepath.Join(t.TempDir(), "vapid.key")
	if err := os.WriteFile(stale, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	remade := newVapidSource(nil, stale, nil)
	if keys, err := remade.Keys(bg); err != nil || keys.Private == "" || keys.Private == "not a key" {
		t.Errorf("the keys over a junk file = (%+v, %v), want a fresh pair", keys, err)
	}
	// a private key that is not one is refused
	if _, err := vapidKeysOf("not a key"); err == nil {
		t.Error("vapidKeysOf of text answered nil, want a refusal")
	}
	if _, err := vapidKeysOf(""); err == nil {
		t.Error("vapidKeysOf of nothing answered nil, want a refusal")
	}
	if _, err := vapidKeysOf("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); err == nil {
		t.Error("vapidKeysOf past the curve's order answered nil, want a refusal")
	}
}

func TestVapidKeysRefusePlacesTheyCannotWrite(t *testing.T) {
	// the key file's place cannot be made
	underFile := t.TempDir()
	file := filepath.Join(underFile, "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := newVapidSource(nil, filepath.Join(file, "vapid.key"), nil)
	if _, err := source.Keys(bg); err == nil {
		t.Error("Keys past an unwritable key file answered nil, want the write's error")
	}
	// a store that refuses the fresh key is only logged at; the pair still answers
	talking := newVapidSource(fakeVapidStore{refuses: true}, filepath.Join(t.TempDir(), "v2.key"),
		func(string, ...any) {})
	if keys, err := talking.Keys(bg); err != nil || keys.Private == "" {
		t.Errorf("Keys past a refusing store = (%+v, %v), want the file's fresh pair", keys, err)
	}
	time.Sleep(20 * time.Millisecond) // the side write to the store lands or not; either is fine
}
