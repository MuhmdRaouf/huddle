// Whether a provider's plugin is still installed and enabled, read from Claude Code's own
// bookkeeping (installed_plugins.json, the scopes' enabledPlugins, the ledger's uninstall
// marker): the session hook and setup decide over it what a removed or disabled plugin leaves
// behind.
package router

import (
	"os"
	"path/filepath"
	"strings"
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
func parseObjectOf(text string, present bool) *jsonNode {
	if !present {
		return nil
	}
	node := parseBodyNode([]byte(text))
	if !node.isObject() {
		return nil
	}
	return node
}

// nodeText is a node's string value, "" when it is not a string (a nil node included).
func nodeText(node *jsonNode) string {
	if node != nil && node.kind == jsonString {
		return node.str
	}
	return ""
}

// scopeFiles is the settings files that hold a scope's `enabledPlugins`.
func scopeFiles(configDir string, install *jsonNode) []string {
	project := nodeText(install.get("projectPath"))
	scope := nodeText(install.get("scope"))
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
		enabled := parseObjectOf(read(file)).get("enabledPlugins")
		if value := enabled.get(key); value != nil && value.kind == jsonBool && !value.boolean {
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
	if value := installed.get("plugins"); value != nil && value.kind != jsonNull {
		// The candidate is stringified and parsed back as a record, so anything but an object
		// lands on the empty object.
		plugins = parseObjectOf(marshalNode(value), true)
		if plugins == nil {
			plugins = &jsonNode{kind: jsonObject, fields: map[string]*jsonNode{}}
		}
	}
	var keys []string
	for _, key := range plugins.keys {
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
		for _, install := range installsOf(plugins.get(key)) {
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
	if value := installed.get("plugins"); value != nil && value.kind != jsonNull {
		plugins = parseObjectOf(marshalNode(value), true)
		if plugins == nil {
			return ""
		}
	}
	for _, key := range plugins.keys {
		if !strings.HasPrefix(key, name+"-plugin-cc@") {
			continue
		}
		for _, install := range installsOf(plugins.get(key)) {
			if path := nodeText(install.get("installPath")); path != "" {
				return path
			}
		}
	}
	return ""
}

// installsOf is one plugin key's installs, each as an object (a malformed one counts as a
// user-scope install).
func installsOf(value *jsonNode) []*jsonNode {
	list := []*jsonNode{{kind: jsonObject, fields: map[string]*jsonNode{}}}
	if value != nil && value.kind == jsonArray {
		list = value.items
	}
	out := make([]*jsonNode, 0, len(list))
	for _, install := range list {
		if install.isObject() {
			out = append(out, install)
		} else {
			out = append(out, &jsonNode{kind: jsonObject, fields: map[string]*jsonNode{}})
		}
	}
	return out
}
