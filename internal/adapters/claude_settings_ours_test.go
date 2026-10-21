package adapters

// Whose values a setup records and an undo restores: the CA and NO_PROXY this huddle's proxy
// mode writes are never the user's "before", however they reached the ledger, while values the
// user had go back (HR-ROUTER-OWNERSHIP defect 2).

import (
	"os"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// ownEnvOf reads the settings file's env back; a file with no env object at all (an undo that
// emptied it) reads as empty.
func ownEnvOf(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := proxyRoot(t, string(data)).get("env")
	got := make(map[string]string)
	if !env.isObject() {
		return got
	}
	for _, key := range env.keys {
		value, _ := env.get(key).text()
		got[key] = value
	}
	return got
}

// caPathOf loads the CA the way an apply does, so the test knows the exact path the plan
// writes into NODE_EXTRA_CA_CERTS.
func caPathOf(t *testing.T, dir string) string {
	t.Helper()
	ca, err := pki.LoadOrCreateRouterCA(t.Context(), dir, time.Now())
	if err != nil {
		t.Fatalf("load the router CA: %v", err)
	}
	return ca.CertPath()
}

// TestASetupRecordsNoneOfItsOwnValuesAsBefore pins the record side: a before-value that is
// exactly what this run's plan writes is ours, and the ledger records no before for it —
// otherwise every later undo restores our own CA and NO_PROXY forever.
func TestASetupRecordsNoneOfItsOwnValuesAsBefore(t *testing.T) {
	caDir := proxyCADir(t)
	caPath := caPathOf(t, caDir)

	t.Run("a run that writes the CA and NO_PROXY it finds records no before for them", func(t *testing.T) {
		path := settingsFilePath(t)
		writeSettingsFile(t, path, "{\n  \"env\": {\n    \"NODE_EXTRA_CA_CERTS\": \""+caPath+"\",\n    \"NO_PROXY\": \"localhost,127.0.0.1\"\n  }\n}\n")
		var delta *SettingsDelta
		proxyAppliedLines(t, path, caDir, ApplyProviderOptions{Record: func(d SettingsDelta) { delta = &d }})
		if delta == nil {
			t.Fatal("the run recorded no delta")
		}
		if delta.CABefore != nil {
			t.Errorf("CABefore = %q; want nil: the CA is ours", *delta.CABefore)
		}
		if delta.NoProxyBefore != nil {
			t.Errorf("NoProxyBefore = %q; want nil: the NO_PROXY is ours", *delta.NoProxyBefore)
		}
	})

	t.Run("the user's own NO_PROXY beside our CA still records a before", func(t *testing.T) {
		path := settingsFilePath(t)
		writeSettingsFile(t, path, "{\n  \"env\": {\n    \"NODE_EXTRA_CA_CERTS\": \""+caPath+"\",\n    \"NO_PROXY\": \"corp.example\"\n  }\n}\n")
		var delta *SettingsDelta
		proxyAppliedLines(t, path, caDir, ApplyProviderOptions{Record: func(d SettingsDelta) { delta = &d }})
		if delta == nil {
			t.Fatal("the run recorded no delta")
		}
		if delta.CABefore != nil {
			t.Errorf("CABefore = %q; want nil: the CA is ours", *delta.CABefore)
		}
		if delta.NoProxyBefore == nil || *delta.NoProxyBefore != "corp.example" {
			t.Errorf("NoProxyBefore = %v; want the user's own entries", delta.NoProxyBefore)
		}
	})
}

// TestTheUndoTakesItsOwnValuesOutOfALedger pins the undo side: a ledger that already carries
// our own CA and NO_PROXY as the before-values (an earlier setup recorded them) is cleaned —
// both keys go — while a user's own values are restored.
func TestTheUndoTakesItsOwnValuesOutOfALedger(t *testing.T) {
	caDir := proxyCADir(t)
	caPath := caPathOf(t, caDir)
	ours := ProxyOurs{CAPath: caPath, NoProxy: "localhost,127.0.0.1"}

	t.Run("a ledger carrying our own values takes both keys out", func(t *testing.T) {
		path := settingsFilePath(t)
		writeSettingsFile(t, path, "{\n  \"env\": {\n    \"HTTPS_PROXY\": \""+routerURL+"\",\n    \"NODE_EXTRA_CA_CERTS\": \""+caPath+"\",\n    \"NO_PROXY\": \"localhost,127.0.0.1\"\n  }\n}\n")
		lines, err := UndoProviderSettings(t.Context(), path, "zai", SettingsUndo{
			ProxyBefore:   nil,
			CABefore:      strPtr(caPath),
			NoProxyBefore: strPtr("localhost,127.0.0.1"),
		}, routerURL, nil, nil, ours)
		if err != nil {
			t.Fatalf("UndoProviderSettings: %v", err)
		}
		assertLines(t, lines,
			"removed HTTPS_PROXY ("+routerURL+")",
			"removed NODE_EXTRA_CA_CERTS",
			"removed NO_PROXY",
		)
		env := ownEnvOf(t, path)
		for _, key := range []string{"HTTPS_PROXY", "NODE_EXTRA_CA_CERTS", "NO_PROXY"} {
			if _, has := env[key]; has {
				t.Errorf("%s = %q; want it gone: the value is ours", key, env[key])
			}
		}
	})

	t.Run("the user's own CA and NO_PROXY go back", func(t *testing.T) {
		path := settingsFilePath(t)
		writeSettingsFile(t, path, "{\n  \"env\": {\n    \"HTTPS_PROXY\": \""+routerURL+"\",\n    \"NODE_EXTRA_CA_CERTS\": \""+caPath+"\",\n    \"NO_PROXY\": \"localhost,127.0.0.1\"\n  }\n}\n")
		lines, err := UndoProviderSettings(t.Context(), path, "zai", SettingsUndo{
			ProxyBefore:   nil,
			CABefore:      strPtr("/etc/ssl/company.pem"),
			NoProxyBefore: strPtr("corp.example,localhost"),
		}, routerURL, nil, nil, ours)
		if err != nil {
			t.Fatalf("UndoProviderSettings: %v", err)
		}
		assertLines(t, lines,
			"removed HTTPS_PROXY ("+routerURL+")",
			"set NODE_EXTRA_CA_CERTS back to /etc/ssl/company.pem",
			"set NO_PROXY back to corp.example,localhost",
		)
		env := ownEnvOf(t, path)
		if env["NODE_EXTRA_CA_CERTS"] != "/etc/ssl/company.pem" {
			t.Fatalf("NODE_EXTRA_CA_CERTS = %q; want the user's own CA", env["NODE_EXTRA_CA_CERTS"])
		}
		if env["NO_PROXY"] != "corp.example,localhost" {
			t.Fatalf("NO_PROXY = %q; want the user's own entries", env["NO_PROXY"])
		}
	})
}
