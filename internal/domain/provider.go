package domain

// Every name the plugin shows its users and everything else provider-specific the shared code
// reads, as data. Each plugin defines its own and wires it into its deps, so the shared code
// spells no provider's names.

// EnvLookup is the environment a provider reads: the process environment's string values.
type EnvLookup map[string]string

// AuthStyle is how the key authenticates a request; today's only style carries it as a Bearer
// token.
type AuthStyle string

const AuthBearer AuthStyle = "bearer"

// ProviderRouter is this plugin's own router: its port, its legacy service label, its health
// path, and the model prefixes it claims.
type ProviderRouter struct {
	// The port the router takes when no env override names one.
	Port int `json:"port"`
	// zai keeps its historical `dev.muhmdraouf.zai-router`; new plugins use
	// `com.muhmdraouf.<name>-router`.
	Label string `json:"label"`
	// The GET path answering `{"ok":true,"name":…,"provider":…}`.
	HealthPath string `json:"healthPath"`
	// A request whose model id starts with one of these goes to the provider; everything else to
	// Anthropic untouched.
	ModelPrefixes []string `json:"modelPrefixes"`
}

// Provider is the provider as a plugin ships it.
type Provider struct {
	// The CLI's name: message prefix `<name>: …`, the synopsis, the temp index dir prefix.
	Name string `json:"name"`
	// The provider as people know it, e.g. "Z.ai GLM"; setup, key and error lines name it.
	Display string `json:"display"`
	// Slash-command prefix, e.g. "/zai:" in `/zai:board`.
	Slash string `json:"slash"`
	// Agent prefix, e.g. "zai:" in `zai:glm-5.3`.
	AgentPrefix string `json:"agentPrefix"`
	// Edit jobs work on branch `<branchPrefix><id>`.
	BranchPrefix string `json:"branchPrefix"`
	// Names the harness in the commit trailer `Worked-by: <model id> via <harness>`.
	Harness string `json:"harness"`
	// The variable naming an exec job's artifacts dir, for the worker and the gates.
	ArtifactsEnv string `json:"artifactsEnv"`
	// Environment-variable prefix: `${envPrefix}_BASE_URL` overrides the endpoint.
	EnvPrefix string `json:"envPrefix"`
	// What runs the model, as setup and worker errors name it.
	WorkerLabel string `json:"workerLabel"`
	// The Anthropic-compatible endpoint.
	BaseURL string `json:"baseUrl"`
	// How the key authenticates a request.
	Auth AuthStyle `json:"auth"`
	// API key environment variables in priority order (qwen also accepts DASHSCOPE_API_KEY).
	KeyEnv []string `json:"keyEnv"`
	// The key file `setup` writes and the worker and router read instead (mode 600).
	KeyFile string `json:"keyFile"`
	// Where the user tops up the account (the provider's billing or top-up page): named when the
	// provider refuses a request for lack of balance or quota.
	BillingURL string `json:"billingUrl"`
	// Where the user creates an API key (the provider's key page): linked from the one-time key
	// page.
	KeysURL string `json:"keysUrl"`
	// Request fields this provider rejects; the router strips them from provider-bound requests.
	Strip []string `json:"strip"`
	// Model caveats brief writers must heed, one sentence each, printed in the brief template.
	Caveats []string `json:"caveats"`
	// This plugin's own router.
	Router ProviderRouter `json:"router"`
}
