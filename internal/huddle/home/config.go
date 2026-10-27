// The home's config.toml: the role this machine plays, the port, the hub's address and the
// per-machine switches. The file is TOML; keys huddle does not know are kept on every rewrite, so
// an owner's notes and a newer release's keys survive an older release writing the file.
package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// HubConfig is the hub's address, for a machine whose role talks to one.
type HubConfig struct {
	URL    string `toml:"url"`
	Listen string `toml:"listen"`
	Host   string `toml:"host"`
}

// RemoteConfig holds the per-machine switches: whether the app may answer permissions and send
// messages, and which tools it may allow. The allow list is patterns; the floor
// (Bash(rm *), Bash(git push --force*), Bash(* secrets*), anything under ~/.ssh, ~/.gnupg or a
// secrets directory) is enforced in code whatever the list says.
type RemoteConfig struct {
	Permissions bool     `toml:"permissions"`
	Messages    bool     `toml:"messages"`
	Allow       []string `toml:"allow"`
}

// CertConfig is the certificate section: which DNS provider answers the DNS-01 challenge for
// the hub's name. The provider is chosen by name, the issuer knows the rest.
type CertConfig struct {
	DNSProvider string `toml:"dns_provider"`
}

// Config is every key huddle owns in config.toml.
type Config struct {
	Role     string       `toml:"role"` // solo, hub or satellite
	Port     int          `toml:"port"` // the chosen port; 0 when none is saved
	PortAuto bool         `toml:"port_auto"`
	Machine  string       `toml:"machine"`
	Hub      HubConfig    `toml:"hub"`
	Cert     CertConfig   `toml:"cert"`
	Remote   RemoteConfig `toml:"remote"`
}

// machineName answers this machine's name: the hostname, fitted to the device-name rule.
func machineName() string {
	name, err := os.Hostname()
	if err != nil {
		name = ""
	}
	return fitMachineName(name)
}

// fitMachineName cuts a hostname at its first dot and fits it to the rule pairing and join-hub
// check (a letter or digit first, then up to 31 letters, digits, spaces, underscores or dashes):
// a default name that fails the rule would refuse every join made without --name.
func fitMachineName(host string) string {
	if at := strings.IndexByte(host, '.'); at > 0 {
		host = host[:at]
	}
	fitted := []byte(host)
	for i, c := range fitted {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && c != ' ' && c != '_' && c != '-' {
			fitted[i] = '-'
		}
	}
	name := strings.TrimLeft(string(fitted), " _-")
	if len(name) > 32 {
		name = name[:32]
	}
	if name == "" {
		return "localhost"
	}
	return name
}

// defaults answers the config a fresh home starts from.
func defaults() Config {
	return Config{
		Role:    "solo",
		Machine: machineName(),
		Remote:  RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}
}

// LoadConfig reads the home's config.toml. A missing or torn file answers the defaults: a config
// problem never keeps a huddle entry from running.
func LoadConfig(h Home) (Config, error) {
	cfg := defaults()
	raw, err := os.ReadFile(h.Config())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if _, err := toml.Decode(string(raw), &cfg); err != nil {
		return defaults(), nil
	}
	if cfg.Role == "" {
		cfg.Role = "solo"
	}
	if cfg.Machine == "" {
		cfg.Machine = machineName()
	}
	if cfg.Remote.Allow == nil {
		cfg.Remote.Allow = []string{"*"}
	}
	return cfg, nil
}

// SaveConfig writes the config back, keeping every unknown key the file holds: the known keys are
// overlaid on what was read, so nothing the owner or a newer release wrote is lost. The write is
// atomic (a temp file beside it, then a rename) and private (0600).
func SaveConfig(h Home, cfg Config) error {
	raw := readRaw(h)
	if cfg.Role == "" {
		cfg.Role = "solo"
	}
	if cfg.Machine == "" {
		cfg.Machine = machineName()
	}
	raw["role"] = cfg.Role
	raw["port"] = cfg.Port
	raw["port_auto"] = cfg.PortAuto
	raw["machine"] = cfg.Machine
	raw["hub"] = hubTable(cfg.Hub)
	raw["cert"] = certTable(cfg.Cert)
	raw["remote"] = remoteTable(cfg.Remote)
	if _, has := raw["notify"]; !has {
		raw["notify"] = map[string]any{}
	}
	return writeTOML(h.Config(), raw)
}

// SavePort records the port in config.toml: auto true marks a port huddle picked itself (it may
// move when another program takes it), false one someone chose (never moved). It answers the file.
func SavePort(h Home, port int, auto bool) (string, error) {
	raw := readRaw(h)
	raw["port"] = port
	if auto {
		raw["port_auto"] = true
	} else {
		delete(raw, "port_auto")
	}
	if _, has := raw["role"]; !has {
		raw["role"] = "solo"
	}
	if _, has := raw["machine"]; !has {
		raw["machine"] = machineName()
	}
	if err := writeTOML(h.Config(), raw); err != nil {
		return "", err
	}
	return h.Config(), nil
}

// ensureConfig writes the default config when the home has none; a file that is already there,
// whole or as the owner left it, is never touched.
func ensureConfig(h Home) error {
	if _, err := os.Stat(h.Config()); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg := defaults()
	raw := map[string]any{
		"role":      cfg.Role,
		"port":      cfg.Port,
		"port_auto": cfg.PortAuto,
		"machine":   cfg.Machine,
		"hub":       hubTable(cfg.Hub),
		"cert":      certTable(cfg.Cert),
		"remote":    remoteTable(cfg.Remote),
		"notify":    map[string]any{},
	}
	return writeTOML(h.Config(), raw)
}

// hubTable is the hub section as a plain map, for overlaying on the raw one.
func hubTable(hub HubConfig) map[string]any {
	return map[string]any{"url": hub.URL, "listen": hub.Listen, "host": hub.Host}
}

// certTable is the cert section as a plain map.
func certTable(cert CertConfig) map[string]any {
	return map[string]any{"dns_provider": cert.DNSProvider}
}

// remoteTable is the remote section as a plain map.
func remoteTable(remote RemoteConfig) map[string]any {
	allow := append([]string{}, remote.Allow...)
	if len(allow) == 0 {
		allow = []string{"*"}
	}
	return map[string]any{"permissions": remote.Permissions, "messages": remote.Messages, "allow": allow}
}

// readRaw answers the file as a plain map (unknown keys included); a missing or torn file is an
// empty map.
func readRaw(h Home) map[string]any {
	raw := map[string]any{}
	if body, err := os.ReadFile(h.Config()); err == nil {
		if _, err := toml.Decode(string(body), &raw); err != nil {
			return map[string]any{}
		}
	}
	return raw
}

// writeTOML encodes the map and writes it atomically with private modes, creating the directory
// the file sits in. The encoder writes map keys sorted, so the same config always renders the
// same bytes.
func writeTOML(path string, raw map[string]any) error {
	var out strings.Builder
	if err := toml.NewEncoder(&out).Encode(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(out.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
