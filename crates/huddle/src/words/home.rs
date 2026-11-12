//! Bare `huddle`, `huddle help` and the Providers block: where the user stands and what to run
//! next. The renderers are pure; `standing` and the `print_*` words do the reading and writing.

use std::io::Write;

use huddle_catalog::providers::providers;
use huddle_config::env::Env;
use huddle_config::paths::{Paths, router_port};
use huddle_config::providers_file::read_enabled;
use huddle_daemon::start::{HEALTH_WAIT, health};
use huddle_keystore::port::platform_default;
use huddle_setup::settings::ledger_path;

use super::version::VERSION;

/// The column the first Next command is padded to.
const NEXT_WIDTH: usize = 23;

const HELP: &str = "\
Setup
  setup                  install huddle
  uninstall              undo the install; settings.json comes back as it was
  version                print the version

Providers
  provider               list providers; huddle provider <name> sets one up

Delegation
  run                    hand a task to another coding CLI
  status                 delegated jobs
  result                 read a job's diff and report, accept or return it
  cancel                 stop a job

Router
  router                 start, stop or check the router
  add-dashboard-host     let a host name open the dashboard
  remove-dashboard-host  take a host name off that list
";

/// One provider's line in the Providers block.
pub struct ProviderRow {
    pub id: &'static str,
    pub display: &'static str,
    pub key_stored: bool,
    pub on: bool,
}

/// Everything bare `huddle` shows.
pub struct Standing {
    pub version: String,
    /// The router's port while it answers.
    pub router_up_on: Option<u16>,
    /// False until `huddle setup` has written its settings ledger.
    pub installed: bool,
    pub providers: Vec<ProviderRow>,
}

/// Escape codes, or nothing at all when the output is not a colour terminal.
#[derive(Clone, Copy)]
pub struct Style {
    color: bool,
}

impl Style {
    pub fn plain() -> Style {
        Style { color: false }
    }

    pub fn for_stdout(env: &Env) -> Style {
        let tty = unsafe { libc::isatty(1) } == 1;
        Style {
            color: tty && env.get("NO_COLOR").is_none_or(str::is_empty),
        }
    }

    fn paint(self, code: &str, text: &str) -> String {
        if self.color {
            format!("\x1b[{code}m{text}\x1b[0m")
        } else {
            text.to_owned()
        }
    }

    fn bold(self, text: &str) -> String {
        self.paint("1", text)
    }

    fn dim(self, text: &str) -> String {
        self.paint("2", text)
    }

    fn green(self, text: &str) -> String {
        self.paint("32", text)
    }
}

pub fn render_home(standing: &Standing, style: Style) -> String {
    let mut text = String::new();
    let version = style.bold(&format!("huddle {}", standing.version));
    match standing.router_up_on {
        Some(port) => {
            let address = format!("127.0.0.1:{port}");
            text.push_str(&format!(
                "{version}  {} {}\n{}  http://{address}\n",
                style.green("router up"),
                style.dim(&format!("on {address}")),
                style.dim("dashboard"),
            ));
        }
        None => text.push_str(&format!("{version}  router stopped\n")),
    }
    text.push('\n');
    text.push_str(&render_providers(&standing.providers, style));
    text.push_str(&format!("\n{}\n", style.bold("Next")));
    let (command, note) = first_next(standing);
    text.push_str(&next_line(&command, &note, style));
    text.push_str(&next_line("huddle status", "delegated jobs", style));
    text.push_str(&next_line("huddle help", "every command", style));
    text
}

/// The Providers heading and one aligned row per provider.
pub fn render_providers(rows: &[ProviderRow], style: Style) -> String {
    let mut text = format!("{}\n", style.bold("Providers"));
    for row in rows {
        let state = match (row.key_stored, row.on) {
            (true, true) => style.green("key stored · on"),
            (true, false) => style.green("key stored · off"),
            (false, _) => style.dim("no key"),
        };
        text.push_str(&format!("  {:<10}{:<16}{state}\n", row.id, row.display));
    }
    text
}

/// The `huddle provider <name>` hint under the Providers block of `huddle provider`.
pub fn render_provider_hint(style: Style) -> String {
    format!(
        "\n  huddle provider <name>   {}\n",
        style.dim("set it up or change it")
    )
}

fn first_next(standing: &Standing) -> (String, String) {
    if !standing.installed {
        return (
            "/huddle:setup in Claude Code".to_owned(),
            "install huddle".to_owned(),
        );
    }
    match standing.providers.iter().find(|row| !row.key_stored) {
        Some(row) => (
            format!("huddle provider {}", row.id),
            "add a provider; asks for its key".to_owned(),
        ),
        None => (
            format!("huddle provider {}", standing.providers[0].id),
            "change a provider's key or remove it".to_owned(),
        ),
    }
}

fn next_line(command: &str, note: &str, style: Style) -> String {
    let pad = NEXT_WIDTH.saturating_sub(command.chars().count()).max(3);
    format!("  {command}{}{}\n", " ".repeat(pad), style.dim(note))
}

/// Reads the router, the install ledger, `providers.toml` and the keystore. A failed read shows
/// as the empty state: a provider without a key, off, a router that does not answer.
pub fn standing(env: &Env) -> Result<Standing, i32> {
    let paths = Paths::from_env(env).map_err(|error| {
        let _ = writeln!(std::io::stderr(), "{error}");
        1
    })?;
    let port = router_port(env).map_err(|error| {
        let _ = writeln!(std::io::stderr(), "{error}");
        2
    })?;
    let ids: Vec<&str> = providers().iter().map(|provider| provider.id).collect();
    let enabled = read_enabled(&paths.config_dir, &ids).unwrap_or_default();
    let store = platform_default(&paths);
    let rows = providers()
        .iter()
        .map(|provider| ProviderRow {
            id: provider.id,
            display: provider.display,
            key_stored: matches!(store.get(provider.id), Ok(Some(_))),
            on: enabled.iter().any(|name| name == provider.id),
        })
        .collect();
    Ok(Standing {
        version: VERSION.trim().to_owned(),
        router_up_on: health(port, HEALTH_WAIT).map(|_| port),
        installed: ledger_path(&paths).exists(),
        providers: rows,
    })
}

/// Bare `huddle`.
pub fn run_home(env: &Env) -> i32 {
    match standing(env) {
        Ok(standing) => {
            let text = render_home(&standing, Style::for_stdout(env));
            let _ = write!(std::io::stdout(), "{text}");
            0
        }
        Err(code) => code,
    }
}

/// `huddle help`.
pub fn run_help() -> i32 {
    let _ = write!(std::io::stdout(), "{HELP}");
    0
}

/// `huddle provider` alone: the block and the hint on stdout, exit 0.
pub fn run_providers(env: &Env) -> i32 {
    match standing(env) {
        Ok(standing) => {
            let style = Style::for_stdout(env);
            let text = render_providers(&standing.providers, style) + &render_provider_hint(style);
            let _ = write!(std::io::stdout(), "{text}");
            0
        }
        Err(code) => code,
    }
}

/// `huddle provider <not a provider>`: the block and the hint on stderr, exit 2. The argument is
/// never printed, since it may be a key.
pub fn run_unknown_provider(env: &Env) -> i32 {
    match standing(env) {
        Ok(standing) => {
            let text = render_providers(&standing.providers, Style::plain())
                + &render_provider_hint(Style::plain());
            let _ = write!(std::io::stderr(), "{text}");
            2
        }
        Err(code) => code,
    }
}

/// An unknown top-level word: one line and the help on stderr, exit 2. The word is never
/// printed, since it may be a key.
pub fn run_unknown_word() -> i32 {
    let _ = write!(std::io::stderr(), "huddle: unknown word\n\n{HELP}");
    2
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: &'static str, key_stored: bool, on: bool) -> ProviderRow {
        ProviderRow {
            id,
            display: "Z.ai GLM",
            key_stored,
            on,
        }
    }

    #[test]
    fn a_colour_style_wraps_states_in_escape_codes_and_plain_does_not() {
        let rows = [row("zai", true, true)];
        let coloured = render_providers(&rows, Style { color: true });
        assert!(coloured.contains("\x1b[32mkey stored · on\x1b[0m"));
        assert!(!render_providers(&rows, Style::plain()).contains('\x1b'));
    }

    #[test]
    fn a_next_command_longer_than_the_column_keeps_three_spaces() {
        let line = next_line(
            "/huddle:setup in Claude Code",
            "install huddle",
            Style::plain(),
        );
        assert_eq!(line, "  /huddle:setup in Claude Code   install huddle\n");
    }
}
