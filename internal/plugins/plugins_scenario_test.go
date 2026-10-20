package plugins

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// A provider plugin works from its launcher: the behaviour a user's session sees, written as
// scenarios over the same rig the launcher smoke runs.

func TestAProviderPluginWorksFromItsLauncher(t *testing.T) {
	bdd.Feature(t, "a provider plugin works from its launcher", func(f *bdd.F) {
		f.Scenario("the help lists the documented commands", func(s *bdd.S) {
			var helps map[string]string
			s.Given("every provider plugin's committed launcher with the binary installed", func(t *testing.T) {
				helps = map[string]string{}
				for _, name := range providers.Names {
					provider, pluginDir := providerOf(t, name)
					home := testkit.TempDir(t, name+"-scenario-help-")
					installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
					stdout, stderr, code := runOne(t, "sh",
						[]string{launcherPath(t, name), name + ".js", "--help"},
						smokeEnv(t, provider, home, nil), home)
					if code != 0 {
						t.Fatalf("%s: the launcher exited %d: %s", name, code, stderr)
					}
					helps[name] = stdout
				}
			})
			s.Then("every help names each documented command", func(t *testing.T) {
				for name, help := range helps {
					for _, command := range documentedCommands {
						if !regexp.MustCompile(`(?m)^ {2}` + command + ` `).MatchString(help) {
							t.Errorf("%s: the help never shows %s:\n%s", name, command, help)
						}
					}
				}
			})
			s.And("the run synopsis and the no-command-of-its-own line with it", func(t *testing.T) {
				for name, help := range helps {
					if !strings.Contains(help, "\n  run <brief.md|->") {
						t.Errorf("%s: the help never shows the run synopsis:\n%s", name, help)
					}
					if thereIsNo := "there is no " + name + " command on PATH"; !strings.Contains(help, thereIsNo) {
						t.Errorf("%s: the help never says %q", name, thereIsNo)
					}
				}
			})
		})

		f.Scenario("a hook never fails the session", func(s *bdd.S) {
			s.Given("a state dir that cannot be made (a file sits where it would go)", func(t *testing.T) {
				for _, name := range providers.Names {
					provider, pluginDir := providerOf(t, name)
					home := testkit.TempDir(t, name+"-scenario-hook-")
					installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
					broken := filepath.Join(home, "not-a-dir")
					if err := os.WriteFile(broken, []byte("a file where the state dir would go"), 0o644); err != nil {
						t.Fatal(err)
					}
					stdout, stderr, code := runOne(t, "sh",
						[]string{launcherPath(t, name), name + ".js", "setup", "--hook"},
						smokeEnv(t, provider, home, map[string]string{
							provider.EnvPrefix + "_STATE_DIR": filepath.Join(broken, "state"),
						}), home)
					if code != 0 || stdout != "" || stderr != "" {
						t.Errorf("%s: setup --hook = (code %d, stdout %q, stderr %q), want silent exit 0", name, code, stdout, stderr)
					}
				}
			})
			s.Then("every provider's setup --hook exited 0 and said nothing", func(t *testing.T) {})
		})

		f.Scenario("board --hook is silent with nothing to report", func(s *bdd.S) {
			s.Given("an empty state dir and the installed binary", func(t *testing.T) {
				for _, name := range providers.Names {
					provider, pluginDir := providerOf(t, name)
					home := testkit.TempDir(t, name+"-scenario-board-")
					installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
					stdout, stderr, code := runOne(t, "sh",
						[]string{launcherPath(t, name), name + ".js", "board", "--hook"},
						smokeEnv(t, provider, home, nil), home)
					if code != 0 || stdout != "" || stderr != "" {
						t.Errorf("%s: board --hook = (code %d, stdout %q, stderr %q), want silent exit 0", name, code, stdout, stderr)
					}
				}
			})
			s.Then("every provider's board --hook exited 0 and said nothing", func(t *testing.T) {})
		})
	})
}

// launcherPath is one provider's committed launcher, resolved (the runs sit in temp dirs).
func launcherPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(pluginDirOf(name), "dist", "run"))
	if err != nil {
		t.Fatalf("resolve the launcher: %v", err)
	}
	return path
}
