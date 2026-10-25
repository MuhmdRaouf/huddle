package outside

// The notifier's and the radar reader's own defaults and refusals, and the JSON helpers that
// lay the settings file out: the pieces that decide without a program to run.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestANotifierWithoutAClockOrEnvironmentReadsTheProcess(t *testing.T) {
	n := &Notifier{}
	if n.now().IsZero() {
		t.Error("now() = the zero time, want the wall clock")
	}
	if n.env("PATH") == "" {
		t.Error("env(PATH) = empty, want the process's own")
	}
}

func TestANotifierWhoseLogCannotBeWrittenSaysSo(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{
		getenv: func(name string) string {
			if name == "HUDDLE_NOTIFY_LOG" {
				return filepath.Join(file, "notify.log")
			}
			return ""
		},
		last: map[string]time.Time{},
	}
	if n.Send("kind", "subject", "title", "body") {
		t.Error("Send past an unwritable log answered true, want false")
	}
}

func TestPrettyJSONLaysValuesOutAndKeepsEmptiesCompact(t *testing.T) {
	if got := prettyJSON("", 1); got != "" {
		t.Errorf("prettyJSON of nothing = %q, want empty", got)
	}
	if got := prettyJSON("[ ]", 1); got != "[]" {
		t.Errorf("prettyJSON of an empty list = %q, want []", got)
	}
	laid := prettyJSON(`{"a":1,"b":[1,2]}`, 0)
	for _, want := range []string{`"a": 1`, `"b": [`, "  1", "]"} {
		if !strings.Contains(laid, want) {
			t.Errorf("prettyJSON = %q, want %q in it", laid, want)
		}
	}
}

func TestScanMembersReadsOnlyWellShapedObjects(t *testing.T) {
	if scanMembers("not an object") != nil {
		t.Error("scanMembers of text answered members, want nil")
	}
	if scanMembers(`{"a"}`) != nil {
		t.Error("scanMembers of a key without its colon answered members, want nil")
	}
	if scanMembers(`{"a":}`) != nil {
		t.Error("scanMembers of a key without its value answered members, want nil")
	}
	if scanMembers(`{"a":1,}`) == nil {
		t.Error("scanMembers of a plain object answered nil")
	}
}

func TestScanStringAndScanValueReadTheTextShapes(t *testing.T) {
	if end, ok := scanString(`plain`, 0); ok || end != 0 {
		t.Errorf("scanString of text = (%d, %v), want not ok", end, ok)
	}
	if end, ok := scanString(`"a\"b" tail`, 0); !ok || end != 6 {
		t.Errorf("scanString past an escaped quote = (%d, %v), want 6 true", end, ok)
	}
	if end, ok := scanString(`"open`, 0); ok {
		t.Errorf("scanString of an unterminated string = (%d, %v), want not ok", end, ok)
	}
	if end, ok := scanValue(`,rest`, 0); ok || end != 0 {
		t.Errorf("scanValue at a comma = (%d, %v), want not ok", end, ok)
	}
	if end, ok := scanValue(`42,x`, 0); !ok || end != 2 {
		t.Errorf("scanValue of a number = (%d, %v), want 2 true", end, ok)
	}
}

func TestARadarReaderWithoutItsOwnWaysReadsTheProcess(t *testing.T) {
	r := &Radar{}
	if r.now().IsZero() {
		t.Error("now() = the zero time, want the wall clock")
	}
	t.Setenv("HUDDLE_RADAR_PROBE", "yes")
	if r.env("HUDDLE_RADAR_PROBE") != "yes" {
		t.Error("env() missed the process's variable")
	}
	// a state dir from nothing: the home's own radar place
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_HOME", "")
	if got := r.StateDir(); got == "" || filepath.Base(got) != "radar" {
		t.Errorf("StateDir() = %q, want the home's radar dir", got)
	}
}

func TestARadarFetchWithoutAClientStillAsksOnce(t *testing.T) {
	r := &Radar{Env: func(string) string { return t.TempDir() }, Now: time.Now} // Client nil on purpose
	if _, ok := r.fetch(context.Background(), 1, "/api/summary"); ok {
		t.Error("fetch of a port nothing answers answered ok")
	}
}

func TestCostBySessionTakesTheDayWhenNoWindowIsNamed(t *testing.T) {
	r := radarOn(t, 200, radarAttributionBody)
	costs, ok := r.CostBySession(context.Background(), "")
	if !ok {
		t.Fatal("CostBySession with no window answered not ok, want the day's answer")
	}
	if costs.Len() == 0 {
		t.Error("the day's costs are empty, want the fake's rows")
	}
	// an answer that is not a list is not a cost table
	weird := radarOn(t, 200, `{"rows":"not a list"}`)
	if _, ok := weird.CostBySession(context.Background(), RangeDay); ok {
		t.Error("CostBySession of a non-list answered ok")
	}
}

func TestTheJavaScriptScalarsReadTheWayTheDashboardDoes(t *testing.T) {
	if got := jsText(json.RawMessage(`[]`), "fallback"); got != "fallback" {
		t.Errorf("jsText of a list = %q, want the fallback", got)
	}
	if got := jsNumber(json.RawMessage(`1e999`)); got != 0 {
		t.Errorf("jsNumber of an overflowing number = %v, want 0", got)
	}
	if got := jsNumber(json.RawMessage(`"2.5"`)); got != 2.5 {
		t.Errorf("jsNumber of a quoted number = %v, want 2.5", got)
	}
	if ptr := jsNumberPtr(json.RawMessage(`"2.5"`)); ptr != nil {
		t.Errorf("jsNumberPtr of a quoted number = %v, want nil", *ptr)
	}
	if ptr := jsNumberPtr(json.RawMessage(`1e999`)); ptr != nil {
		t.Errorf("jsNumberPtr of an overflowing number = %v, want nil", *ptr)
	}
	if ptr := jsNumberPtr(json.RawMessage(`3.25`)); ptr == nil || *ptr != 3.25 {
		t.Errorf("jsNumberPtr of a number = %v, want 3.25", ptr)
	}
}

func TestEnabledPluginsOfANonObjectIsNone(t *testing.T) {
	if enabledPlugins("settings") != nil {
		t.Error("enabledPlugins of text answered a table, want nil")
	}
	if enabledPlugins(map[string]any{"other": 1}) != nil {
		t.Error("enabledPlugins without the key answered a table, want nil")
	}
}

func TestAPluginPresentWhenTheUserSettingsAreMissing(t *testing.T) {
	config, env := world(t)
	install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
	presence := PluginPresence(context.Background(), env, "huddle", nil)
	if presence != PresencePresent {
		t.Errorf("PluginPresence with no user settings = %v, want present", presence)
	}
	// a config dir with neither a CLAUDE_CONFIG_DIR nor a HOME still lands somewhere
	home := ClaudeConfigDir(map[string]string{})
	if home == "" {
		t.Error("configDir with no environment answered empty")
	}
}

func TestTheRemovalWatchChecksOnItsOwnClock(t *testing.T) {
	config, env := world(t)
	install(t, config, `{"other@market":`+installedEntry+`}`) // huddle is not among them
	gone := make(chan string, 1)
	watch := WatchRemoval(RemovalOptions{
		Env:      env,
		Plugin:   "huddle",
		Interval: 10 * time.Millisecond,
		Gone:     func(kind Gone) { gone <- string(kind) },
	})
	t.Cleanup(watch.Stop)
	select {
	case verdict := <-gone:
		if verdict != string(PresenceUninstalled) {
			t.Errorf("gone = %q, want uninstalled", verdict)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never acted on its own clock")
	}
}

func TestARadarAnswerThatIsNotOkIsNotAnAnswer(t *testing.T) {
	unhappy := radarOn(t, 503, `<html>down</html>`)
	if _, ok := unhappy.Alerts(context.Background()); ok {
		t.Error("Alerts of a 503 answered ok")
	}
}
