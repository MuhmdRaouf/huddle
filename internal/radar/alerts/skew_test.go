package alerts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// onlyPluginBehind keeps the plugin_behind alerts.
func onlyPluginBehind(alerts []shared.Alert) []shared.Alert {
	var out []shared.Alert
	for _, alert := range alerts {
		if alert.Kind == shared.AlertPluginBehind {
			out = append(out, alert)
		}
	}
	return out
}

// TestPluginBehindFollowsEachSessionStartsCommits: a start that finds the plugin cache and the
// binary on different commits raises one plugin_behind alert naming both and the update line; a
// second skewed start keeps that one alert; a start that finds them equal clears it; a machine
// with no plugin install raises nothing.
func TestPluginBehindFollowsEachSessionStartsCommits(t *testing.T) {
	ctx := context.Background()
	env := domain.EnvLookup{"RADAR_HOME": t.TempDir(), "HOME": t.TempDir()}
	detectNow := func() []shared.Alert {
		in := input()
		in.PluginSkew = ReadPluginSkew(ctx, env)
		return onlyPluginBehind(detect(in))
	}
	record := func(cache, binary string, at int64) {
		t.Helper()
		if err := RecordPluginCommits(ctx, env, cache, binary, at); err != nil {
			t.Fatalf("record %q %q: %v", cache, binary, err)
		}
	}

	record("052c8e49c9ff", "38e9e40e321e", now-5*minMs)
	first := detectNow()
	if len(first) != 1 {
		t.Fatalf("skewed start: alerts = %+v, want one", first)
	}
	alert := first[0]
	if alert.ID != "plugin_behind:052c8e49c9ff:38e9e40e321e" || alert.Since != now-5*minMs ||
		alert.Severity != shared.SeverityWarn || alert.SessionID != "" {
		t.Errorf("alert = %+v", alert)
	}
	wantDetail := "Claude Code's plugin cache holds huddle 052c8e49c9ff, the binary is 38e9e40e321e: " +
		"run `claude plugin update huddle@muhmdraouf`, then start a new session"
	if alert.Detail != wantDetail {
		t.Errorf("detail = %q, want %q", alert.Detail, wantDetail)
	}

	record("052c8e49c9ff", "38e9e40e321e", now)
	if again := detectNow(); len(again) != 1 || again[0].ID != alert.ID || again[0].Since != alert.Since {
		t.Fatalf("second skewed start: alerts = %+v, want the first one unchanged", again)
	}

	record("38e9e40e321e", "38e9e40e321e", now)
	if cleared := detectNow(); len(cleared) != 0 {
		t.Fatalf("equal start: alerts = %+v, want none", cleared)
	}

	record("052c8e49c9ff", "38e9e40e321e", now)
	record("", "38e9e40e321e", now)
	if none := detectNow(); len(none) != 0 {
		t.Fatalf("no plugin install: alerts = %+v, want none", none)
	}
	record("052c8e49c9ff", "", now)
	if none := detectNow(); len(none) != 0 {
		t.Fatalf("no source-built binary: alerts = %+v, want none", none)
	}
}

// TestPluginSkewReadsNothingFromAnUnreadableFile: a skew file that is not JSON, or names no
// commits, raises nothing.
func TestPluginSkewReadsNothingFromAnUnreadableFile(t *testing.T) {
	ctx := context.Background()
	env := domain.EnvLookup{"RADAR_HOME": t.TempDir(), "HOME": t.TempDir()}
	for _, body := range []string{"not json", `{"cache":"","binary":""}`} {
		if err := os.WriteFile(filepath.Join(env["RADAR_HOME"], "plugin-skew.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if skew := ReadPluginSkew(ctx, env); skew != nil {
			t.Errorf("file %q: skew = %+v, want nil", body, skew)
		}
	}
}
