package lane

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func routerEnv(t *testing.T, providersJSON string) domain.EnvLookup {
	t.Helper()
	dir := t.TempDir()
	if providersJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(providersJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return domain.EnvLookup{"HUDDLE_ROUTER_HOME": dir, "HUDDLE_ROUTER_PORT": "21999"}
}

func TestChoicesListTheEnabledProvidersWithMainThenFlash(t *testing.T) {
	choices := Choices(routerEnv(t, `{"providers":["kimi","zai"]}`))
	if len(choices) != 2 || choices[0].Provider != "zai" || choices[0].Models[0] != "glm-5.3" || choices[0].Models[1] != "glm-5.3-flash" {
		t.Fatalf("choices = %+v, want zai first with glm-5.3 then glm-5.3-flash, then kimi", choices)
	}
}

func TestChoicesOfAnUnsetUpRouterAreNone(t *testing.T) {
	if choices := Choices(routerEnv(t, "")); len(choices) != 0 {
		t.Fatalf("choices = %+v, want none", choices)
	}
}

func TestURLNamesTheRoutersPort(t *testing.T) {
	url, err := URL(routerEnv(t, ""))
	if err != nil || url != "http://127.0.0.1:21999" {
		t.Fatalf("URL = %q (%v), want the port the environment names", url, err)
	}
}
