# Incidents

Each incident, its cause, and the test that now stops it. A guard named here must exist:
`crates/huddle/tests/docs_guards.rs:every_guard_an_incident_names_exists` checks.

## 2026-10-17, a worker rewrote the owner's Claude Code config

- **What happened:** a worker ran `export HOME=$T CLAUDE_CONFIG_DIR="$HOME/.claude"` on one line; the shell
  expanded `$HOME` before the export took effect, so a plugin command changed the real `~/.claude`.
- **Cause:** the shell expands `$HOME` on the same command line before `export` sets it.
- **Guard:** crates/huddle-testkit/tests/temp_home.rs:owner_variables_never_reach_a_child,
  crates/huddle-testkit/tests/source_guard.rs:no_test_reaches_live_ports_or_the_process_environment
- **Rule:** every child process gets its environment from the test kit's temp home, with literal
  temp paths.

## 2026-10-20, tests retired the live zai router twice

- **What happened:** a test binary started a router with the real HOME, and that process took the
  live router over; workers got HTTP 400.
- **Cause:** an unsandboxed test binary became a daemon that replaced the owner's router.
- **Guard:** crates/huddle-daemon/src/start.rs:a_test_build_can_never_spawn_the_daemon,
  crates/huddle-daemon/src/start.rs:a_cargo_test_harness_path_is_recognised,
  crates/huddle-testkit/tests/temp_home.rs:a_child_runs_under_the_private_home_only
- **Rule:** a test binary never starts the daemon, and every test child runs under its own temp
  home.

## 2026-10-21, a test reached the live router's port

- **What happened:** tests named the port the owner's live router serves on.
- **Cause:** a literal port where a free port belongs.
- **Guard:** crates/huddle-testkit/tests/source_guard.rs:no_test_reaches_live_ports_or_the_process_environment,
  crates/huddle-testkit/tests/temp_home.rs:parallel_callers_never_get_the_same_port
- **Rule:** tests bind an ephemeral port or lease one from the test kit, never a literal the live
  router may hold.

## 2026-10-21, an isolated run wrote the owner's config

- **What happened:** a run with a temp HOME but no isolated state root wrote its state into the
  real user's config directories.
- **Cause:** the state and XDG variables outrank HOME, and the run left them unset.
- **Guard:** crates/huddle-testkit/tests/temp_home.rs:every_state_variable_points_inside_the_home
- **Rule:** isolation sets HOME, CLAUDE_CONFIG_DIR and every XDG_* variable to the
  temp home, and the kit checks every one of them.

## 2026-10-21, a script signalled 76 of the owner's processes

- **What happened:** `pgrep -f <pattern> -n` took `-n` as a second pattern on macOS, matched every process with
  `-n` in its arguments, and the script sent SIGTERM to all of them.
- **Cause:** macOS `pgrep -f` reads a second argument as another pattern, so `-n` matched far more than the
  script's own children.
- **Guard:** crates/huddle-testkit/tests/guard.rs:a_process_left_running_is_killed_and_fails_the_run
- **Rule:** a test signals only processes it started; anything a run leaves under its temp root is
  killed by the guard and fails the run.

## 2026-10-21, the router started twice, and remove left our proxy values

- **What happened:** a start compared a symlinked path with a resolved one and started a second
  router; removal recorded the setup's own CA and NO_PROXY values as the user's.
- **Cause:** the ownership check compared the path as typed, and removal treated the setup's own
  values as the user's.
- **Guard:** crates/huddle-daemon/tests/start_update.rs:same_binary_compares_resolved_files,
  crates/huddle-setup/tests/settings_undo.rs:a_foreign_proxy_set_after_the_patch_is_left_alone,
  crates/huddle-setup/tests/settings_undo.rs:undo_restores_an_unusually_formatted_file_byte_for_byte
- **Rule:** ownership compares resolved files, and remove takes out only the values we wrote.

## 2026-10-21, a router routed claude traffic through another router

- **What happened:** every `claude-*` request through the router failed for six hours, answered with 502s, and
  nothing said why.
- **Cause:** the daemon's `HTTPS_PROXY` and `HTTP_PROXY` named another router, one on a port that
  was not the known one, so the daemon tunnelled its claude traffic through it; a failing
  Anthropic path raised no event.
- **Guard:** crates/huddle-daemon/src/nochain.rs:a_loopback_proxy_answering_our_health_name_is_dropped,
  crates/huddle-daemon/src/nochain.rs:the_canonical_address_and_our_own_port_are_routers,
  crates/huddle-router/tests/upstream_proxy.rs:a_proxy_naming_huddle_router_is_no_proxy,
  crates/huddle-router/tests/dispatch.rs:a_streak_raises_once_and_recovers_once
- **Rule:** a daemon drops a proxy that answers a router health path and goes direct; a proxy that
  is no router is kept. A failing Anthropic path raises one event within a minute and recovers on
  the first success.

## 2026-10-25, a healthy router had no pid to show

- **What happened:** a caller saw the daemon answer its health path, read `run/router.toml` and
  found no file. It passed on every rerun alone.
- **Cause:** the daemon served its listener before its record landed, so under load a caller could
  see it healthy before the record existed. `router start` and `router status` read the pid the
  same way.
- **Guard:** crates/huddle-daemon/tests/serve_loop.rs:the_record_lands_before_the_first_health_answer_and_goes_on_stop
- **Rule:** the record lands before the first health answer, so a router seen healthy always has a
  pid to show.

## 2026-10-26, a deploy found no router to hand over from

- **What happened:** a deploy's takeover found no handover socket at the run path, because the old
  daemon's stop removed the path that was by then the taker's socket.
- **Cause:** a daemon that hands over retires its socket file, and its stop removed the same path a
  second time.
- **Guard:** crates/huddle-daemon/src/handover.rs:a_stale_socket_file_is_cleared_before_the_listen,
  crates/huddle-daemon/src/handover.rs:two_takers_race_and_only_one_gets_the_socket
- **Rule:** a stale socket file is cleared once, before the listen, and retirement never happens
  twice.

## 2026-10-27, two test binaries leased the same port

- **What happened:** a test failed because another binary's child answered on the port it had
  leased: the lease lived in one map per test binary, and the suite runs package binaries in
  parallel.
- **Cause:** a per-process lease under parallel test binaries.
- **Guard:** crates/huddle-testkit/tests/temp_home.rs:a_stale_lease_is_taken_over_and_a_live_one_is_skipped,
  crates/huddle-testkit/tests/temp_home.rs:a_listener_holds_its_port_from_the_moment_it_is_learned
- **Rule:** a leased port is held by a listener from the moment it is learned, a stale lease is
  taken over and a live one is skipped.

## 2026-10-27, a handover on a busy machine refused dials

- **What happened:** under five parallel build jobs the takeover saw refused dials.
- **Cause:** the taker ran its setup before it served, while the old daemon had closed its copy of
  the port.
- **Guard:** crates/huddle-daemon/tests/serve_loop.rs:a_port_is_held_while_the_backlog_absorbs_many_dials_before_any_answer,
  crates/huddle-daemon/src/handover.rs:a_takeover_receives_a_listener_that_really_serves
- **Rule:** the listening port is bound with a backlog that absorbs dials before any answer, so a
  start under load refuses no dial.

## 2026-11-01, e2e runs left their routers running

- **What happened:** router processes from one worktree's test runs ran for days and loaded the
  machine, so later gates went flaky.
- **Cause:** a killed run never reached its teardown, and nothing swept the run's folder for
  processes still under it.
- **Guard:** crates/huddle-testkit/tests/guard.rs:a_process_left_running_is_killed_and_fails_the_run,
  crates/huddle-testkit/tests/guard.rs:a_dead_runs_folder_is_swept_and_a_live_one_is_left_alone
- **Rule:** a run ends with the processes it started stopped; the guard kills anything left under
  the run's temp root, sweeps a dead run's folder and fails a run that leaves a process behind.

## 2026-11-02, the session-start files were half-written on a busy disk

- **What happened:** under four processes writing and syncing files, a session start left some of
  the managed agent files missing and a stray temp file beside each of them.
- **Cause:** every file cost its syncs, the hook's budget ended mid-write, and the process exited
  in the middle of a write; nothing removed the half-written file.
- **Guard:** crates/huddle-setup/tests/agent_files.rs:sync_writes_one_file_per_family_and_second_run_changes_nothing,
  crates/huddle-setup/tests/agent_files.rs:an_empty_file_is_written_again_and_a_managed_stale_file_is_removed,
  crates/huddle-setup/tests/agent_files.rs:a_users_file_with_a_wanted_name_stays_untouched,
  crates/huddle-config/tests/paths.rs:an_atomic_write_replaces_the_file_with_the_mode_and_leaves_no_temp,
  crates/huddle-config/tests/paths.rs:leftover_temp_names_of_a_dead_process_never_block_a_write
- **Rule:** a file the session start regenerates is written through an atomic rename; the next run
  writes an empty managed file again, removes a stale managed one and leaves the user's files alone.

## 2026-11-10, a test's child process outlived the suite on Linux

- **What happened:** the Linux test job passed every test, then failed: the suite guard found a
  `sleep` started by a test still running after the run.
- **Cause:** the guard matched a process only when its ps line named the run directory followed by
  `/`; a child that names it only as `TMPDIR=<dir>` in its environment was never seen, so a loop
  script was killed and its children were left alive. Linux shows a process's environment in ps;
  macOS hides it for system binaries, so only Linux showed the leak.
- **Guard:** crates/huddle-testkit/tests/guard.rs:a_loop_that_keeps_forking_leaves_no_process_behind
- **Rule:** the guard matches the run directory as a whole path, and looks again after every kill
  until a look finds no process of the run.

## 2026-11-10, a test script was busy at exec on Linux

- **What happened:** a fake harness script failed to start with "Text file busy" (ETXTBSY) on the
  Linux test job.
- **Cause:** the test process wrote the script itself; another test thread forked while the write
  was open, and the forked child held the write handle until it exec'd, so the exec of the script
  failed. Renaming the file afterwards keeps the same file, so it does not help.
- **Guard:** crates/huddle-testkit/tests/script.rs:a_script_runs_right_after_its_write_while_other_threads_fork
- **Rule:** a test writes a file it will execute through huddle_testkit::script::write_executable,
  which writes it from a child process, so the test process never holds a write handle on it.

## 2026-11-11, intercepted model requests wrote no route events

- **What happened:** `huddle provider <p> usage` and the events file showed a handful of requests
  while router.log listed thousands: 2361 model requests, 4 events.
- **Cause:** the request context and the event sink were put on a request only in the plain-HTTP
  handler. Claude Code's traffic arrives through CONNECT and is decrypted, and that path built the
  request without either, so every Anthropic and provider-lane model request skipped its event.
- **Guard:** crates/huddle-router/tests/connect_decrypt.rs:t5_decrypted_model_requests_reach_the_history
- **Rule:** a model request carries its context and the event sink from the moment it arrives,
  whichever way it arrived.

## 2026-11-12, provider setup said it could not reach a provider that answered

- **What happened:** `/huddle:zai:setup` printed "could not reach Z.ai GLM to check the key" while Z.ai
  was reachable and answered every request.
- **Cause:** the key check folded every status other than 2xx, 401, 402, 403 and 429 into the same
  verdict as a network failure, and the report printed "could not reach" for both. The status the
  provider sent was lost.
- **Guard:** crates/huddle/tests/provider_setup_word.rs:k10_an_unexpected_status_is_named_in_the_report
- **Rule:** a check that got an answer reports what the answer was; "could not reach" means no
  answer came back.

## 2026-11-12, the Anthropic path failed for seven minutes and the log could not say where

- **What happened:** from 15:34Z to 15:41Z every request from the router to `api.anthropic.com` failed with
  `ETIMEDOUT` after about 16.3 s, and it recovered by itself within seconds of the user running
  `huddle setup`. At 15:49Z the router was replaced by a new one with nothing in the log saying why.
- **Cause:** unknown at the time. The failure lines carried only a code, so DNS, connect, TLS and
  waiting for the answer looked the same, and no line named the peer, the start reason or the exit reason.
- **Guard:** crates/huddle-router/tests/k16_failure_logs.rs and crates/huddle-daemon/tests/k16_lifecycle.rs
  (the phase and peer on a failed line, the failing and recovered lines, the probe, the start, handover
  and exit lines); crates/huddle/tests/k16_prompt_hook_log.rs (the hook's reason for a restart).
- **Rule:** a failure line names where it failed, and a router names who started it and why it ended.

## 2026-11-12, test runs left routers and key page helpers running

- **What happened:** after repeated test runs the machine's load went past 300: test routers and
  key page helpers were still running long after their tests ended.
- **Cause:** a test home started a router or a key page helper and nothing stopped it when the
  test ended or failed.
- **Guard:** crates/huddle/tests/k8_key_page.rs:k8_a_test_home_stops_the_helper_it_started stops
  the helpers; no guard yet stops a router with its test.
- **Rule:** a test home stops every process it started when it is dropped.

## 2026-11-12, the prompt hook started a second router next to a busy one

- **What happened:** under load the hook's 50 ms probe missed a live router, the hook ran `router start`,
  and the start spawned a second `router serve` that the singleton lock refused. The logs held a
  restart line and a refused serve for a router that never stopped.
- **Cause:** a missed probe was read as a dead router, and nothing checked the router lock before the spawn.
- **Guard:** crates/huddle/tests/prompt_probe.rs:b2_a_held_lock_means_no_second_router and
  b2_a_free_lock_still_starts_the_router; crates/huddle-daemon/tests/start_probe.rs:b2_a_start_that_loses_the_lock_waits_for_the_winner
- **Rule:** a slow answer is not a dead router: nothing spawns while the router lock is held.

## 2026-11-13, every session start removed the providers

- **What happened:** after `/huddle:zai:setup` the next Claude Code session emptied `providers.toml`,
  deleted the zai key from the Keychain and removed the zai agents; setup then asked for the key again.
- **Cause:** the SessionStart hook still removed each enabled provider whose own provider plugin was
  not installed. Since 0.0.6 the providers live in the huddle plugin, so no provider plugin exists and
  every session start removed every provider.
- **Guard:** crates/huddle/tests/session_providers.rs:a_session_start_keeps_a_provider_set_up_inside_huddle
- **Rule:** only `/huddle:<p>:remove` turns a provider off; a hook never removes one.
