// Whether a provider's plugin is still installed and enabled, read from Claude Code's own
// bookkeeping (installed_plugins.json, the scopes' enabledPlugins, the ledger's uninstall
// marker): the session hook and setup decide over it what a removed or disabled plugin leaves
// behind.
package router

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Presence is whether the plugin is still installed: present (installed and enabled in some
// scope), disabled (installed but turned off everywhere it is installed), absent (not installed,
// or its data dir — and with it the marker — is gone) or unknown (the files cannot be read).
type Presence string

const (
	PresencePresent  Presence = "present"
	PresenceDisabled Presence = "disabled"
	PresenceAbsent   Presence = "absent"
	PresenceUnknown  Presence = "unknown"
)

// ReadText reads a file's text, false when it cannot be read.
type ReadText func(path string) (string, bool)

// readText is the production reader.
func readText(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// parseObjectOf is the text as a JSON object, nil when there is no text or it is not one.
func parseObjectOf(text string, present bool) *jsfmt.Value {
	if !present {
		return nil
	}
	node := parseBodyNode([]byte(text))
	if !node.IsObject() {
		return nil
	}
	return node
}

// scopeFiles is the settings files that hold a scope's `enabledPlugins`.
func scopeFiles(configDir string, install *jsfmt.Value) []string {
	project, _ := install.Get("projectPath").TextOf()
	scope, _ := install.Get("scope").TextOf()
	switch {
	case scope == "project" && project != "":
		return []string{filepath.Join(project, ".claude", "settings.json")}
	case scope == "local" && project != "":
		return []string{filepath.Join(project, ".claude", "settings.local.json")}
	default:
		return []string{filepath.Join(configDir, "settings.json")}
	}
}

// enabledIn is whether one scope's settings leave the plugin on. A plugin with no
// `enabledPlugins` entry follows its `defaultEnabled`, which defaults to true and which these
// plugins never set (Claude Code docs: settings-reference "enabledPlugins: A plugin with no entry
// at any scope falls back to its defaultEnabled value"; manifest-reference "defaultEnabled …
// Defaults to true"). An unreadable file says nothing, so it counts as the default too.
func enabledIn(files []string, key string, read ReadText) bool {
	for _, file := range files {
		enabled := parseObjectOf(read(file)).Get("enabledPlugins")
		if value := enabled.Get(key); value != nil && value.Kind() == jsfmt.KindBool && !value.Boolean() {
			return false
		}
	}
	return true
}

// PluginPresence is whether `<name>-plugin-cc@<marketplace>` is still installed and enabled, from
// installed_plugins.json, the scopes' enabledPlugins and the `installed` marker in the plugin's
// data dir. A nil read is the production reader.
func PluginPresence(name string, configDir string, marker *string, read ReadText) Presence {
	if read == nil {
		read = readText
	}
	installed := parseObjectOf(read(filepath.Join(configDir, "plugins", "installed_plugins.json")))
	if installed == nil {
		return PresenceUnknown
	}
	plugins := installed
	if value := installed.Get("plugins"); value != nil && value.Kind() != jsfmt.KindNull {
		// The candidate is stringified and parsed back as a record, so anything but an object
		// lands on the empty object.
		plugins = parseObjectOf(value.EncodeLiteral(), true)
		if plugins == nil {
			plugins = jsfmt.Object(nil, nil)
		}
	}
	var keys []string
	for _, key := range plugins.Keys() {
		if strings.HasPrefix(key, name+"-plugin-cc@") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return PresenceAbsent
	}
	if marker != nil {
		if _, ok := read(*marker); !ok {
			return PresenceAbsent
		}
	}
	for _, key := range keys {
		for _, install := range installsOf(plugins.Get(key)) {
			if enabledIn(scopeFiles(configDir, install), key, read) {
				return PresencePresent
			}
		}
	}
	return PresenceDisabled
}

// PluginInstallPath is the first install path Claude Code's installed_plugins.json records for
// `<name>-plugin-cc@<marketplace>`: the installed plugin's directory, where its dist bundles and
// agents sit. "" when the file names none.
func PluginInstallPath(name string, configDir string, read ReadText) string {
	if read == nil {
		read = readText
	}
	installed := parseObjectOf(read(filepath.Join(configDir, "plugins", "installed_plugins.json")))
	if installed == nil {
		return ""
	}
	plugins := installed
	if value := installed.Get("plugins"); value != nil && value.Kind() != jsfmt.KindNull {
		plugins = parseObjectOf(value.EncodeLiteral(), true)
		if plugins == nil {
			return ""
		}
	}
	for _, key := range plugins.Keys() {
		if !strings.HasPrefix(key, name+"-plugin-cc@") {
			continue
		}
		for _, install := range installsOf(plugins.Get(key)) {
			if path, _ := install.Get("installPath").TextOf(); path != "" {
				return path
			}
		}
	}
	return ""
}

// installsOf is one plugin key's installs, each as an object (a malformed one counts as a
// user-scope install).
func installsOf(value *jsfmt.Value) []*jsfmt.Value {
	list := []*jsfmt.Value{jsfmt.Object(nil, nil)}
	if value.IsArray() {
		list = value.Items()
	}
	out := make([]*jsfmt.Value, 0, len(list))
	for _, install := range list {
		if install.IsObject() {
			out = append(out, install)
		} else {
			out = append(out, jsfmt.Object(nil, nil))
		}
	}
	return out
}
