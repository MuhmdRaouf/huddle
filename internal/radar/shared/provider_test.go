package shared

import "testing"

func TestProviderOf(t *testing.T) {
	t.Run("groups model ids by prefix, case-insensitively", func(t *testing.T) {
		cases := []struct{ model, want string }{
			{"claude-opus-5-5", "Anthropic"},
			{"GLM-4.7", "Z.ai"},
			{"kimi-k2", "Moonshot"},
			{"deepseek-v4", "DeepSeek"},
			{"MiniMax-M2", "MiniMax"},
			{"qwen3-max", "Qwen"},
			{"QwQ-32B", "Qwen"},
		}
		for _, tt := range cases {
			if got := ProviderOf(tt.model); got != tt.want {
				t.Errorf("ProviderOf(%q) = %q, want %q", tt.model, got, tt.want)
			}
		}
	})

	t.Run("falls back to other for anything unknown, missing or empty", func(t *testing.T) {
		cases := []string{"gpt-9", ""}
		for _, model := range cases {
			if got := ProviderOf(model); got != "other" {
				t.Errorf("ProviderOf(%q) = %q, want other", model, got)
			}
		}
	})
}

func TestDefaultUpstream(t *testing.T) {
	t.Run("is the Anthropic API", func(t *testing.T) {
		if DefaultUpstream != "https://api.anthropic.com" {
			t.Errorf("DefaultUpstream = %q, want https://api.anthropic.com", DefaultUpstream)
		}
	})
}

func TestUpstreamHost(t *testing.T) {
	t.Run("shows a host for a URL and the raw value for something unparseable", func(t *testing.T) {
		cases := []struct{ raw, want string }{
			{"https://api.anthropic.com", "api.anthropic.com"},
			{"http://localhost:8787/v1", "localhost:8787"},
			{"not a url at all", "not a url at all"},
		}
		for _, tt := range cases {
			if got := UpstreamHost(tt.raw); got != tt.want {
				t.Errorf("UpstreamHost(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		}
	})

	t.Run("names a request with no upstream as a direct Claude Code call", func(t *testing.T) {
		// "" is the caller's stand-in for a missing (undefined or null) upstream.
		if got := UpstreamHost(""); got != DirectUpstream {
			t.Errorf("UpstreamHost(\"\") = %q, want %q", got, DirectUpstream)
		}
	})
}

func TestPluginLabel(t *testing.T) {
	t.Run("shows a provider plugin's display name, unknown names as they are", func(t *testing.T) {
		cases := []struct{ plugin, want string }{
			{"zai", "Z.ai"},
			{"kimi", "Kimi"},
			{"deepseek", "DeepSeek"},
			{"minimax", "MiniMax"},
			{"qwen", "Qwen"},
			{"other", "other"},
		}
		for _, tt := range cases {
			if got := PluginLabel(tt.plugin); got != tt.want {
				t.Errorf("PluginLabel(%q) = %q, want %q", tt.plugin, got, tt.want)
			}
		}
	})
}

func TestScopeLabel(t *testing.T) {
	t.Run("reads a budget scope the way people do", func(t *testing.T) {
		cases := []struct{ scope, want string }{
			{"total", "Total"},
			{"provider:zai", "Z.ai"},
			{"provider:unknown", "unknown"},
			{"kimi", "Kimi"},
		}
		for _, tt := range cases {
			if got := ScopeLabel(tt.scope); got != tt.want {
				t.Errorf("ScopeLabel(%q) = %q, want %q", tt.scope, got, tt.want)
			}
		}
	})
}
