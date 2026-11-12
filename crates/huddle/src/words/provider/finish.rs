//! The steps that finish a provider setup once a key is in hand: the key checked with the
//! provider, the lane and the `/model` entries written, the router started and asked to serve
//! the provider. The result is data; `render_lines` turns it into the setup word's report. The
//! key is read by the check alone and never reaches an outcome, a line or an error.

use std::fmt;
use std::path::Path;
use std::time::Duration;

use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::{self, Paths};
use huddle_daemon::start::{StartOptions, router_key_check, start};
use huddle_keystore::port::{KeystoreError, SecretString, platform_default};
use huddle_router::dispatch::os_roots;
use huddle_router::keycheck::{Verdict, check_key, ping_model};
use huddle_router::upstream::{IDLE_TIMEOUT, UpstreamClient, UpstreamConfig};
use huddle_setup::agent_files::{self, AgentChange};
use huddle_setup::env_file::is_router_url;
use huddle_setup::{provider_setup, settings};

/// How long a start waits for a spawned daemon's health answer.
const START_BUDGET: Duration = Duration::from_millis(4000);

/// How long the lane check waits for a reload that is still catching up.
const LANE_WAIT: Duration = Duration::from_secs(8);

/// What the report says about Claude Code's settings when no run of the huddle setup word is
/// on record.
const LEDGER_LINE: &str =
    "run /huddle:setup first so Claude Code goes through the router, then restart it";

/// The line a changed setup adds last: the /model entries are new, so one restart shows them.
pub const RESTART_LINE: &str = "Restart Claude Code once so /model shows the new models.";

/// What the provider said about the key.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum KeyVerdict {
    /// The provider answered 2xx.
    Accepted,
    /// The key is good but the provider takes no requests yet (quota or balance).
    Limited,
    /// The provider rejected the key.
    Refused,
    /// The provider could not be asked or answered something else.
    Unknown,
}

impl KeyVerdict {
    /// Setup goes on for every verdict but a refusal.
    fn lets_setup_go_on(self) -> bool {
        self != KeyVerdict::Refused
    }
}

/// The steps of a setup, in the order an outcome lists them.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StepName {
    /// The key was written to the key store.
    KeyStored,
    /// The provider was asked whether it takes the key.
    ProviderCheck,
    /// The router serves the provider's lane and Claude Code's settings lead to the router.
    RouterRoutes,
    /// The lane is recorded and the provider's models are in `/model`.
    ModelEntries,
    /// The provider's two agents are in Claude's agents folder.
    Agents,
}

/// How one step ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StepState {
    /// The step did its job.
    Done,
    /// The step went on with a caveat (a limited key, a provider that could not be reached).
    Warned,
    /// The step did not do its job.
    Failed,
    /// The step did not run: it was not asked for, or an earlier step stopped setup.
    Skipped,
}

/// One step and what it has to say, as lines the setup report prints.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Step {
    pub name: StepName,
    pub state: StepState,
    /// Report lines the step produced; the key check's line comes from the verdict instead.
    pub lines: Vec<String>,
}

/// Everything a finished setup knows, without any text but the steps' own lines.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Outcome {
    pub verdict: KeyVerdict,
    /// Five steps: key stored, provider check, router routes the provider, `/model` entries,
    /// agents.
    pub steps: Vec<Step>,
    /// The provider is usable from Claude Code once it restarts.
    pub ready: bool,
    /// The setup changed `providers.toml` or `/model` entries, so Claude Code needs a restart.
    pub changed: bool,
}

impl Outcome {
    /// The step with this name; every outcome lists all four.
    pub fn step(&self, name: StepName) -> Option<&Step> {
        self.steps.iter().find(|step| step.name == name)
    }
}

/// A step that refuses outright; carries no key.
#[derive(Debug)]
pub enum FinishError {
    /// The key could not be written to the key store.
    #[allow(dead_code)]
    Keystore(KeystoreError),
    /// Recording the lane, the `/model` entries or the router port failed.
    Setup(String),
}

impl fmt::Display for FinishError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            FinishError::Keystore(error) => write!(f, "{error}"),
            FinishError::Setup(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for FinishError {}

/// Finishes the setup with a key the user just entered, and stores it unless the provider
/// refused it: a refused key never replaces a stored one. Callers beyond the setup word use
/// this entry point.
#[allow(dead_code)]
pub fn store_and_finish(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &Path,
    key: SecretString,
) -> Result<Outcome, FinishError> {
    let verdict = check_provider_key(provider, env, &key);
    if !verdict.lets_setup_go_on() {
        return Ok(stopped(verdict));
    }
    platform_default(paths)
        .set(provider.id, &key)
        .map_err(FinishError::Keystore)?;
    let mut outcome = wire(provider, env, paths, executable, verdict)?;
    set_state(&mut outcome, StepName::KeyStored, StepState::Done);
    Ok(outcome)
}

/// Checks the key with the provider and, unless the provider refused it, records the lane and
/// the `/model` entries, starts the router from `executable` and waits for it to serve the
/// provider. A refused key turns nothing on.
pub fn finish(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &Path,
    key: &SecretString,
) -> Result<Outcome, FinishError> {
    let verdict = check_provider_key(provider, env, key);
    if !verdict.lets_setup_go_on() {
        return Ok(stopped(verdict));
    }
    wire(provider, env, paths, executable, verdict)
}

/// The outcome of a key the provider refused: nothing stored, nothing turned on.
fn stopped(verdict: KeyVerdict) -> Outcome {
    Outcome {
        verdict,
        steps: vec![
            step(StepName::KeyStored, StepState::Skipped, Vec::new()),
            step(StepName::ProviderCheck, check_state(verdict), Vec::new()),
            step(StepName::RouterRoutes, StepState::Skipped, Vec::new()),
            step(StepName::ModelEntries, StepState::Skipped, Vec::new()),
            step(StepName::Agents, StepState::Skipped, Vec::new()),
        ],
        ready: false,
        changed: false,
    }
}

/// Records the lane and the `/model` entries, starts the router and waits for it to serve the
/// provider; the key is already in the store or the environment.
fn wire(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &Path,
    verdict: KeyVerdict,
) -> Result<Outcome, FinishError> {
    let mut steps = vec![
        step(StepName::KeyStored, StepState::Skipped, Vec::new()),
        step(StepName::ProviderCheck, check_state(verdict), Vec::new()),
    ];
    let enabled = provider_setup::enable(paths, provider)
        .map_err(|error| FinishError::Setup(error.to_string()))?;
    let router = route(provider, env, paths, executable)?;
    let ready = router.state == StepState::Done;
    steps.push(router);
    steps.push(step(StepName::ModelEntries, StepState::Done, enabled.lines));
    steps.push(agents_step(provider, paths));
    Ok(Outcome {
        verdict,
        steps,
        ready,
        changed: enabled.changed,
    })
}

/// The setup word's report lines, in the order the steps ran: the key store line, the provider's
/// answer, the lane and `/model` lines, the router lines, and the restart line last when a
/// ready setup changed something.
pub fn render_lines(outcome: &Outcome, provider: &Provider) -> Vec<String> {
    let mut lines = Vec::new();
    if outcome.step(StepName::KeyStored).map(|s| s.state) == Some(StepState::Done) {
        lines.push("key: stored".to_owned());
    }
    lines.push(verdict_line(outcome.verdict, provider));
    for name in [
        StepName::ModelEntries,
        StepName::Agents,
        StepName::RouterRoutes,
    ] {
        if let Some(step) = outcome.step(name) {
            lines.extend(step.lines.iter().cloned());
        }
    }
    if outcome.ready && outcome.changed {
        lines.push(RESTART_LINE.to_owned());
    }
    lines
}

/// Writes the provider's agents. A failure is a warned step: the lane works without them.
fn agents_step(provider: &Provider, paths: &Paths) -> Step {
    match agent_files::sync_provider(paths, provider) {
        Ok(AgentChange { done, kept }) => {
            let mut lines = Vec::new();
            if !done.is_empty() {
                lines.push(format!("agents: {}", done.join(", ")));
            }
            lines.extend(
                kept.iter()
                    .map(|name| format!("{name} exists and is not huddle's; left as it is")),
            );
            let state = if kept.is_empty() {
                StepState::Done
            } else {
                StepState::Warned
            };
            step(StepName::Agents, state, lines)
        }
        Err(error) => step(
            StepName::Agents,
            StepState::Warned,
            vec![format!("could not write the agents: {error}")],
        ),
    }
}

fn step(name: StepName, state: StepState, lines: Vec<String>) -> Step {
    Step { name, state, lines }
}

#[allow(dead_code)]
fn set_state(outcome: &mut Outcome, name: StepName, state: StepState) {
    if let Some(step) = outcome.steps.iter_mut().find(|step| step.name == name) {
        step.state = state;
    }
}

fn check_state(verdict: KeyVerdict) -> StepState {
    match verdict {
        KeyVerdict::Accepted => StepState::Done,
        KeyVerdict::Limited | KeyVerdict::Unknown => StepState::Warned,
        KeyVerdict::Refused => StepState::Failed,
    }
}

fn verdict_line(verdict: KeyVerdict, provider: &Provider) -> String {
    let display = provider.display;
    match verdict {
        KeyVerdict::Accepted => format!("key: {display} accepted the key."),
        KeyVerdict::Limited => format!(
            "key: {display} knows the key but takes no requests yet (quota or balance): {}",
            provider.billing_url
        ),
        KeyVerdict::Refused => format!("key: {display} refused the stored key; enter a new one."),
        KeyVerdict::Unknown => {
            format!("key: could not reach {display} to check the key; continuing.")
        }
    }
}

/// One small request to the provider with the key: the answer's status alone decides the verdict.
fn check_provider_key(provider: &'static Provider, env: &Env, key: &SecretString) -> KeyVerdict {
    let client = UpstreamClient::new(UpstreamConfig {
        env: env.clone(),
        roots: os_roots(&|_| {}),
        idle: IDLE_TIMEOUT,
    });
    let model = ping_model(provider);
    let base = check_base_url(provider, env);
    let verdict = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map(|runtime| runtime.block_on(check_key(&base, key.expose(), model, &client)))
        .unwrap_or(Verdict::Unknown);
    match verdict {
        Verdict::Accepted => KeyVerdict::Accepted,
        Verdict::Limited => KeyVerdict::Limited,
        Verdict::Refused => KeyVerdict::Refused,
        Verdict::Unknown => KeyVerdict::Unknown,
    }
}

/// The endpoint the check asks: `<P>_ROUTER_URL`, else `<P>_BASE_URL`, else the catalog's.
fn check_base_url(provider: &Provider, env: &Env) -> String {
    let prefix = provider.env_prefix;
    let router_knob = format!("{prefix}_ROUTER_URL");
    let base_knob = format!("{prefix}_BASE_URL");
    let set = |name: &str| {
        env.get(name)
            .map(str::trim)
            .filter(|value| !value.is_empty())
    };
    set(&router_knob)
        .or_else(|| set(&base_knob))
        .map_or_else(|| provider.base_url.to_owned(), str::to_owned)
}

/// Starts the router, waits for it to serve the provider and checks that Claude Code's settings
/// lead to the router. A router that does not serve it is a failed step with its lines.
fn route(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &Path,
) -> Result<Step, FinishError> {
    let port = paths::router_port(env).map_err(|error| FinishError::Setup(error.to_string()))?;
    let mut lines = Vec::new();
    let mut ready = true;
    let options = StartOptions {
        env,
        paths,
        port,
        executable: executable.to_path_buf(),
        budget: START_BUDGET,
    };
    match start(&options) {
        Err(error) => {
            lines.push(format!("the router did not start: {error}"));
            ready = false;
        }
        Ok(_) => match router_key_check(provider.id, provider.display, port, LANE_WAIT) {
            Ok(line) => lines.push(line),
            Err(error) => {
                lines.push(error.to_string());
                ready = false;
            }
        },
    }
    if !settings::ledger_path(paths).exists() {
        lines.push(LEDGER_LINE.to_owned());
        ready = false;
    }
    if let Some(base_url) = settings_base_url(paths)
        && !is_router_url(&base_url, port)
    {
        lines.push(format!(
            "not routed: ANTHROPIC_BASE_URL is {base_url}; the router at \
             http://127.0.0.1:{port} never sees these requests."
        ));
        ready = false;
    }
    let state = if ready {
        StepState::Done
    } else {
        StepState::Failed
    };
    Ok(step(StepName::RouterRoutes, state, lines))
}

/// The `ANTHROPIC_BASE_URL` settings.json carries, when it is a string. A missing or malformed
/// file reads as none.
fn settings_base_url(paths: &Paths) -> Option<String> {
    let (_, doc) = settings::load_doc(&settings::settings_path(paths)).ok()?;
    doc.get("env")?
        .get("ANTHROPIC_BASE_URL")?
        .as_str()
        .map(str::to_owned)
}
#[cfg(test)]
mod tests {
    use super::*;
    use huddle_catalog::providers::provider_by_id;
    use huddle_testkit::fake_upstream::{FakeUpstream, Step as Plan};
    use huddle_testkit::temp_home::TempHome;

    const KEY: &str = "abcdefghijklmnop";

    /// A binary that cannot start: the router step fails at once and the test stays off every
    /// real process.
    const NO_BINARY: &str = "/nonexistent/huddle";

    fn zai() -> &'static Provider {
        provider_by_id("zai").expect("zai")
    }

    fn upstream_answering(status: u16) -> FakeUpstream {
        tokio::runtime::Runtime::new()
            .expect("runtime")
            .block_on(FakeUpstream::start(move |_, _| Plan::answer(status, "{}")))
    }

    /// The temp home's env with the provider's endpoint pointed at the fake.
    fn world(home: &TempHome, upstream: &FakeUpstream) -> (Env, Paths) {
        let mut pairs = home.env();
        pairs.push(("ZAI_BASE_URL".to_owned(), upstream.url()));
        let refs: Vec<(&str, &str)> = pairs
            .iter()
            .map(|(k, v)| (k.as_str(), v.as_str()))
            .collect();
        let env = Env::with(&refs);
        let paths = Paths::from_env(&env).expect("paths");
        (env, paths)
    }

    fn secret() -> SecretString {
        SecretString::new(KEY.to_owned())
    }

    #[test]
    fn k3_finish_reports_each_step_in_order() {
        let home = TempHome::new();
        let upstream = upstream_answering(200);
        let (env, paths) = world(&home, &upstream);
        let outcome = finish(zai(), &env, &paths, Path::new(NO_BINARY), &secret()).expect("finish");
        let names: Vec<StepName> = outcome.steps.iter().map(|s| s.name).collect();
        assert_eq!(
            names,
            [
                StepName::KeyStored,
                StepName::ProviderCheck,
                StepName::RouterRoutes,
                StepName::ModelEntries,
                StepName::Agents
            ]
        );
        assert_eq!(outcome.verdict, KeyVerdict::Accepted);
        assert_eq!(
            outcome.step(StepName::ProviderCheck).map(|s| s.state),
            Some(StepState::Done)
        );
        assert_eq!(
            outcome.step(StepName::ModelEntries).map(|s| s.state),
            Some(StepState::Done)
        );
        assert_eq!(
            outcome.step(StepName::RouterRoutes).map(|s| s.state),
            Some(StepState::Failed),
            "the binary cannot start, so the router does not serve the provider"
        );
        assert!(!outcome.ready);
        assert!(outcome.changed);
        let lines = render_lines(&outcome, zai());
        assert_eq!(lines[0], "key: Z.ai GLM accepted the key.");
        assert!(lines.iter().all(|line| !line.contains(KEY)), "{lines:?}");
    }

    #[test]
    fn k3_a_refused_key_turns_nothing_on() {
        let home = TempHome::new();
        let upstream = upstream_answering(401);
        let (env, paths) = world(&home, &upstream);
        let outcome = finish(zai(), &env, &paths, Path::new(NO_BINARY), &secret()).expect("finish");
        assert_eq!(outcome.verdict, KeyVerdict::Refused);
        assert!(!outcome.ready && !outcome.changed);
        assert_eq!(outcome.steps.len(), 5);
        assert!(!paths.config_dir.join("providers.toml").exists());
        assert!(
            outcome
                .step(StepName::ModelEntries)
                .is_some_and(|s| s.state == StepState::Skipped)
        );
        assert!(!home.root().join(".claude/settings.json").exists());
    }

    #[test]
    fn k3_store_and_finish_stores_the_key_then_finishes() {
        let home = TempHome::new();
        let upstream = upstream_answering(200);
        let (env, paths) = world(&home, &upstream);
        let outcome =
            store_and_finish(zai(), &env, &paths, Path::new(NO_BINARY), secret()).expect("finish");
        assert_eq!(
            outcome.step(StepName::KeyStored).map(|s| s.state),
            Some(StepState::Done)
        );
        let stored = platform_default(&paths).get("zai").expect("get");
        assert_eq!(stored.map(|s| s.expose().to_owned()).as_deref(), Some(KEY));
        let lines = render_lines(&outcome, zai());
        assert_eq!(lines[0], "key: stored");
    }

    #[test]
    fn a_refused_key_never_replaces_the_stored_one() {
        let home = TempHome::new();
        let upstream = upstream_answering(401);
        let (env, paths) = world(&home, &upstream);
        platform_default(&paths)
            .set("zai", &SecretString::new("working-key-0000".to_owned()))
            .expect("seed");

        let outcome =
            store_and_finish(zai(), &env, &paths, Path::new(NO_BINARY), secret()).expect("finish");

        assert_eq!(outcome.verdict, KeyVerdict::Refused);
        assert_eq!(
            outcome.step(StepName::KeyStored).map(|s| s.state),
            Some(StepState::Skipped)
        );
        let stored = platform_default(&paths).get("zai").expect("get");
        assert_eq!(
            stored.map(|s| s.expose().to_owned()).as_deref(),
            Some("working-key-0000")
        );
    }
}
