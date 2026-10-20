package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The repository ships six plugins: huddle itself and the five provider plugins — one marketplace,
// one release manifest every launcher pins, and no leftover link to the repository they came from.

// repoRoot is the repository root, three levels above this package's directory.
const repoRoot = "../../.."

// marketplaceEntry is one listing of the marketplace.
type marketplaceEntry struct {
	Name         string   `json:"name"`
	Source       string   `json:"source"`
	Dependencies []string `json:"dependencies"`
}

// marketplace is the repository's .claude-plugin/marketplace.json.
type marketplace struct {
	Name    string             `json:"name"`
	Plugins []marketplaceEntry `json:"plugins"`
}

// releaseAsset is one target's entry of the release manifest.
type releaseAsset struct {
	SHA256 string `json:"sha256"`
}

// releaseManifest is the committed release/v<VERSION>.json a launcher pins.
type releaseManifest struct {
	Version string                  `json:"version"`
	Assets  map[string]releaseAsset `json:"assets"`
}

// launcherVersionLine reads the version a launcher installs: the `bin=$home/<version>/huddle` line.
var launcherVersionLine = regexp.MustCompile(`(?m)^bin=\$home/([^/]+)/huddle$`)

// launcherWantLines reads a launcher's checksum table: one `  <target>) want=<hex> ;;` line per target.
var launcherWantLines = regexp.MustCompile(`(?m)^  (\S+)\) want=([0-9a-f]{64}) ;;$`)

// TestTheRepositoryShipsSixPlugins runs the marketplace, launcher and repository scenarios.
func TestTheRepositoryShipsSixPlugins(t *testing.T) {
	bdd.Feature(t, "the repository ships six plugins", func(f *bdd.F) {
		f.Scenario("the marketplace lists huddle and the five providers, each source a directory with a plugin.json",
			func(s *bdd.S) {
				var market marketplace
				s.Given("the repository's marketplace file", func(t *testing.T) {
					raw, err := os.ReadFile(filepath.Join(repoRoot, ".claude-plugin", "marketplace.json"))
					if err != nil {
						t.Fatalf("read the marketplace: %v", err)
					}
					if err := json.Unmarshal(raw, &market); err != nil {
						t.Fatalf("parse the marketplace: %v", err)
					}
				})
				s.Then("the marketplace is named muhmdraouf and lists six plugins", func(t *testing.T) {
					if market.Name != "muhmdraouf" {
						t.Errorf("marketplace name is %q, want muhmdraouf", market.Name)
					}
					if len(market.Plugins) != 6 {
						t.Errorf("the marketplace lists %d plugins, want 6", len(market.Plugins))
					}
				})
				s.And("its entries are huddle and the five provider plugins", func(t *testing.T) {
					want := map[string]bool{"huddle": true}
					for _, name := range providers.Names {
						want[name+"-plugin-cc"] = true
					}
					got := map[string]bool{}
					for _, entry := range market.Plugins {
						got[entry.Name] = true
					}
					for name := range want {
						if !got[name] {
							t.Errorf("the marketplace does not list %s", name)
						}
					}
					for name := range got {
						if !want[name] {
							t.Errorf("the marketplace lists %s, which is not one of the six plugins", name)
						}
					}
				})
				s.And("every entry's source is a directory with a plugin.json", func(t *testing.T) {
					for _, entry := range market.Plugins {
						dir := filepath.Join(repoRoot, entry.Source)
						info, err := os.Stat(dir)
						if err != nil {
							t.Errorf("%s: source %s: %v", entry.Name, entry.Source, err)
							continue
						}
						if !info.IsDir() {
							t.Errorf("%s: source %s is not a directory", entry.Name, entry.Source)
							continue
						}
						if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
							t.Errorf("%s: source %s has no .claude-plugin/plugin.json: %v", entry.Name, entry.Source, err)
						}
					}
				})
			})

		f.Scenario("every provider plugin depends on huddle", func(s *bdd.S) {
			var market marketplace
			s.Given("the repository's marketplace file", func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join(repoRoot, ".claude-plugin", "marketplace.json"))
				if err != nil {
					t.Fatalf("read the marketplace: %v", err)
				}
				if err := json.Unmarshal(raw, &market); err != nil {
					t.Fatalf("parse the marketplace: %v", err)
				}
			})
			s.Then("every provider entry names huddle in its dependencies", func(t *testing.T) {
				for _, entry := range market.Plugins {
					if entry.Name == "huddle" {
						continue
					}
					if len(entry.Dependencies) != 1 || entry.Dependencies[0] != "huddle" {
						t.Errorf("%s: dependencies %v, want [huddle]", entry.Name, entry.Dependencies)
					}
				}
			})
		})

		f.Scenario("every provider launcher runs the huddle binary pinned by the release manifest", func(s *bdd.S) {
			var (
				version  string
				wants    map[string]string
				manifest releaseManifest
			)
			s.Given("each provider plugin's committed launcher", func(t *testing.T) {
				wants = map[string]string{}
				for _, name := range providers.Names {
					raw, err := os.ReadFile(filepath.Join(pluginsRoot, name+"-plugin-cc", "dist", "run"))
					if err != nil {
						t.Fatalf("%s: read the launcher: %v", name, err)
					}
					script := string(raw)
					match := launcherVersionLine.FindStringSubmatch(script)
					if match == nil {
						t.Fatalf("%s: the launcher names no versioned huddle binary", name)
					}
					if version == "" {
						version = match[1]
					}
					if match[1] != version {
						t.Errorf("%s pins huddle %s, the other launchers pin %s", name, match[1], version)
					}
					for _, want := range launcherWantLines.FindAllStringSubmatch(script, -1) {
						if prior, ok := wants[want[1]]; ok && prior != want[2] {
							t.Errorf("%s pins %s at %s, another launcher pins %s", name, want[1], want[2], prior)
						}
						wants[want[1]] = want[2]
					}
				}
				if len(wants) != 4 {
					t.Errorf("the launchers pin %d targets, want the four release targets", len(wants))
				}
			})
			s.When("the pinned version names a release manifest", func(t *testing.T) {
				path := filepath.Join(repoRoot, "release", "v"+version+".json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if err := json.Unmarshal(raw, &manifest); err != nil {
					t.Fatalf("parse %s: %v", path, err)
				}
				if manifest.Version != version {
					t.Fatalf("the manifest pins version %q, want %q", manifest.Version, version)
				}
			})
			s.Then("every launcher's checksum table is the manifest's", func(t *testing.T) {
				for target, asset := range manifest.Assets {
					got, ok := wants[target]
					if !ok {
						t.Errorf("the launchers pin no %s digest", target)
						continue
					}
					if got != asset.SHA256 {
						t.Errorf("%s: the launchers pin %s, the manifest pins %s", target, got, asset.SHA256)
					}
				}
				for target := range wants {
					if _, ok := manifest.Assets[target]; !ok {
						t.Errorf("the launchers pin %s, which the manifest does not list", target)
					}
				}
			})
		})

		f.Scenario("no file in the repository names the claude-code-plugins repo except as the retired link",
			func(s *bdd.S) {
				s.Given("every file under plugins/", func(t *testing.T) {
					root := filepath.Join(repoRoot, "plugins")
					err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if entry.IsDir() {
							return nil
						}
						raw, err := os.ReadFile(path)
						if err != nil {
							return err
						}
						if strings.Contains(string(raw), "MuhmdRaouf/claude-code-plugins") {
							t.Errorf("%s names MuhmdRaouf/claude-code-plugins", path)
						}
						return nil
					})
					if err != nil {
						t.Fatalf("walk %s: %v", root, err)
					}
				})
				s.Then("none of them does", func(t *testing.T) {})
			})
	})
}
