package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func mp(segments ...string) string {
	return modulePath + "/" + strings.Join(segments, "/")
}

func mr(segments ...string) string {
	return moduleRoot + "/" + strings.Join(segments, "/")
}

func TestCheckReportsEveryViolation(t *testing.T) {
	files := []FileImports{
		// Every layer's allowed imports, module-internal and third-party.
		{File: "internal/domain/a/a.go", Layer: LayerDomain, Imports: []string{
			"fmt", "gopkg.in/yaml.v3", "github.com/bmatcuk/doublestar/v4", mp("domain/b"),
		}},
		{File: "internal/ports/ports.go", Layer: LayerPorts, Imports: []string{mp("domain/b")}},
		{File: "internal/app/app.go", Layer: LayerApp, Imports: []string{mp("domain/b"), mp("ports")}},
		{File: "internal/adapters/a/a.go", Layer: LayerAdapters, Imports: []string{
			mp("domain/b"), mp("ports"), mp("adapters/c"), "github.com/bmatcuk/doublestar/v4",
		}},
		{File: "internal/render/render.go", Layer: LayerRender, Imports: []string{
			mp("domain/b"), mp("ports"), mp("app"), mp("render"),
		}},
		{File: "internal/authpage/authpage.go", Layer: LayerAuthpage, Imports: []string{
			mp("domain/b"), mp("ports"), mp("adapters/c"),
		}},
		{File: "internal/router/router.go", Layer: LayerRouter, Imports: []string{
			mp("domain/b"), mp("ports"), mp("adapters/c"), mp("router"),
		}},
		// The module's packages outside the engine root count as the layer outside lists them
		// under: internal/pki is the adapters layer, so the router and adapters may import it.
		{File: "internal/router/connect.go", Layer: LayerRouter, Imports: []string{mr("internal/pki")}},
		{File: "internal/adapters/claude_settings.go", Layer: LayerAdapters, Imports: []string{mr("internal/pki")}},
		{File: "internal/providers/providers.go", Layer: LayerProviders, Imports: []string{mp("domain/b")}},
		{File: "internal/cli/cli.go", Layer: LayerCli, Imports: []string{
			mp("domain/b"), mp("ports"), mp("app"), mp("adapters/c"),
			mp("render"), mp("authpage"), mp("router"), mp("providers"),
			mp("cli"), "gopkg.in/yaml.v3",
		}},
		{File: "entry/main.go", Layer: LayerCmd, Imports: []string{
			mp("cli"), mp("router"), mp("providers"), mp("domain/b"), mp("adapters/c"),
		}},
		{File: "internal/testkit/testkit.go", Layer: LayerTestkit, Imports: []string{
			mp("domain/b"), mp("ports"), mp("app"), mp("adapters/c"),
			mp("render"), mp("authpage"), mp("router"), mp("providers"),
			mp("testkit/builder"),
		}},
		{File: "internal/archtest/archtest.go", Layer: LayerArchtest, Imports: []string{"go/parser"}},
		{File: "scripts/covergate/main.go", Layer: LayerScripts, Imports: []string{"fmt"}},

		// Violations: one per forbidden edge.
		{File: "bad/ports.go", Layer: LayerPorts, Imports: []string{mp("ports")}},
		{File: "bad/ports_thirdparty.go", Layer: LayerPorts, Imports: []string{"github.com/spf13/cobra"}},
		{File: "bad/app_adapters.go", Layer: LayerApp, Imports: []string{mp("adapters/c")}},
		{File: "bad/app_app.go", Layer: LayerApp, Imports: []string{mp("app/other")}},
		{File: "bad/adapters_app.go", Layer: LayerAdapters, Imports: []string{mp("app")}},
		{File: "bad/render_authpage.go", Layer: LayerRender, Imports: []string{mp("authpage")}},
		{File: "bad/authpage_render.go", Layer: LayerAuthpage, Imports: []string{mp("render")}},
		{File: "bad/router_render.go", Layer: LayerRouter, Imports: []string{mp("render")}},
		{File: "bad/providers_ports.go", Layer: LayerProviders, Imports: []string{mp("ports")}},
		{File: "bad/providers_thirdparty.go", Layer: LayerProviders, Imports: []string{"github.com/spf13/cobra"}},
		{File: "bad/cli_cmd.go", Layer: LayerCli, Imports: []string{mp("entry")}},
		{File: "bad/cli_testkit.go", Layer: LayerCli, Imports: []string{mp("testkit")}},
		{File: "bad/cli_scripts.go", Layer: LayerCli, Imports: []string{mp("scripts/covergate")}},
		{File: "bad/cmd_app.go", Layer: LayerCmd, Imports: []string{mp("app")}},
		{File: "bad/cmd_ports.go", Layer: LayerCmd, Imports: []string{mp("ports")}},
		{File: "bad/testkit_cli.go", Layer: LayerTestkit, Imports: []string{mp("cli")}},
		{File: "bad/testkit_cmd.go", Layer: LayerTestkit, Imports: []string{mp("entry")}},
		{File: "bad/testkit_scripts.go", Layer: LayerTestkit, Imports: []string{mp("scripts/covergate")}},
		{File: "bad/domain_sys.go", Layer: LayerDomain, Imports: []string{"golang.org/x/sys/unix"}},
		{File: "bad/domain_unknown.go", Layer: LayerDomain, Imports: []string{"github.com/spf13/cobra"}},
		{File: "bad/domain_router.go", Layer: LayerDomain, Imports: []string{mp("router")}},
		{File: "bad/domain_pki.go", Layer: LayerDomain, Imports: []string{mr("internal/pki")}},
		{File: "bad/adapters_outside.go", Layer: LayerAdapters, Imports: []string{mr("internal/radar/hook")}},
		{File: "bad/adapters_thirdparty.go", Layer: LayerAdapters, Imports: []string{"github.com/spf13/cobra"}},
		{File: "bad/archtest_domain.go", Layer: LayerArchtest, Imports: []string{mp("domain/b")}},
		{File: "bad/scripts_domain.go", Layer: LayerScripts, Imports: []string{mp("domain/b")}},
	}

	want := []string{
		"bad/ports.go: ports must not import " + mp("ports"),
		"bad/ports_thirdparty.go: ports must not import github.com/spf13/cobra",
		"bad/app_adapters.go: app must not import " + mp("adapters/c"),
		"bad/app_app.go: app must not import " + mp("app/other"),
		"bad/adapters_app.go: adapters must not import " + mp("app"),
		"bad/render_authpage.go: render must not import " + mp("authpage"),
		"bad/authpage_render.go: authpage must not import " + mp("render"),
		"bad/router_render.go: router must not import " + mp("render"),
		"bad/providers_ports.go: providers must not import " + mp("ports"),
		"bad/providers_thirdparty.go: providers must not import github.com/spf13/cobra",
		"bad/cli_cmd.go: cli must not import " + mp("entry"),
		"bad/cli_testkit.go: cli must not import " + mp("testkit"),
		"bad/cli_scripts.go: cli must not import " + mp("scripts/covergate"),
		"bad/cmd_app.go: entry must not import " + mp("app"),
		"bad/cmd_ports.go: entry must not import " + mp("ports"),
		"bad/testkit_cli.go: testkit must not import " + mp("cli"),
		"bad/testkit_cmd.go: testkit must not import " + mp("entry"),
		"bad/testkit_scripts.go: testkit must not import " + mp("scripts/covergate"),
		"bad/domain_sys.go: domain must not import golang.org/x/sys/unix",
		"bad/domain_unknown.go: domain must not import github.com/spf13/cobra",
		"bad/domain_router.go: domain must not import " + mp("router"),
		"bad/domain_pki.go: domain must not import " + mr("internal/pki"),
		"bad/adapters_outside.go: adapters must not import " + mr("internal/radar/hook"),
		"bad/adapters_thirdparty.go: adapters must not import github.com/spf13/cobra",
		"bad/archtest_domain.go: archtest must not import " + mp("domain/b"),
		"bad/scripts_domain.go: scripts must not import " + mp("domain/b"),
	}

	got := Check(files)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Check() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCheckCleanListHasNoViolations(t *testing.T) {
	files := []FileImports{
		{File: "internal/domain/b/b.go", Layer: LayerDomain, Imports: []string{"fmt", "gopkg.in/yaml.v3"}},
		{File: "internal/app/app.go", Layer: LayerApp, Imports: []string{mp("domain/b"), mp("ports")}},
		{File: "entry/main.go", Layer: LayerCmd, Imports: []string{mp("cli")}},
		{File: "internal/router/upstream.go", Layer: LayerRouter, Imports: []string{"github.com/andybalholm/brotli"}},
	}
	if got := Check(files); len(got) != 0 {
		t.Errorf("Check() = %v, want no violations", got)
	}
}

func TestTreeFollowsTheLayerRule(t *testing.T) {
	files, err := walkTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no non-test Go files found under internal/core")
	}
	for _, msg := range Check(files) {
		t.Error(msg)
	}
}

// walkTree collects FileImports for every non-test .go file under the engine root (internal/core).
func walkTree() ([]FileImports, error) {
	const root = ".."
	var files []FileImports
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		layer, ok := classify(path.Dir(rel))
		if !ok {
			return fmt.Errorf("%s: cannot classify package layer", rel)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		imports := make([]string, 0, len(parsed.Imports))
		for _, imp := range parsed.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			imports = append(imports, value)
		}
		files = append(files, FileImports{File: rel, Layer: layer, Imports: imports})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
