# Test plan

One page listing every huddle feature the owner named, each beside the automated test that proves it.
Run everything from the repo root: `go run ./scripts/tmpguard -count=1 ./...`, `bun run check`, `bun run e2e`.

## Features and their proof

| Feature | Proof | What it asserts |
| --- | --- | --- |
| Install from the launcher | `cmd/huddle/fresh_install_test.go:TestFreshInstall`; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/install` | The launcher downloads the binary from a release server, checks it against the manifest's digest and runs it on a PATH with no JS runtime. |
| First session starts the server | `cmd/huddle/first_session_e2e_test.go:TestE2EFirstSessionStartsHuddle` | A fresh install's SessionStart hook, with no channel and no `HUDDLE_URL`, starts the server through the launcher and it answers `/health`. |
| Hub on a local name over TLS | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `hub_role_and_import`, `hub_up`) | A hub named `hub.example.test` serves its shell over TLS, verified against the CA that signed the pair, with no hosts-file edit. |
| Local CA and `huddle trust` | `internal/huddle/pki/pki_test.go:TestTheRootIsCreatedOnceAndReused`; `internal/huddle/cli/trust_test.go:TestTrustInstallsTheRootAndMarksItAsHuddles`; `web/e2e/fulltest.spec.ts` "a hub home with no import serves a leaf its own root signs" | The root is minted once and kept 0600, installed into the store once and marked as huddle's; a home with no import serves a leaf only its own root vouches for. |
| Imported certificate (`huddle cert --import`) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/hub_role_and_import`; `internal/huddle/cli/cert_test.go:TestCertImportInstallsThePairTheOwnerHolds` | The imported pair is installed, named for the hub's host, and serves the TLS listener. |
| Phone trust (`huddle trust --phone`) | `internal/huddle/cli/trust_phone_test.go:TestTrustPhoneServesTheGuidePrintsTheLinkAndInstallsNothing`; `internal/huddle/phone/helper_test.go:TestTheHelperServesItsThreePathsAndRefusesEverythingElse`; `web/e2e/phone.spec.ts` "the guide shows the iOS steps, the fingerprint and the Android one hidden" | The guide serves the root, the profile and the QR link on loopback, prints the root's fingerprint, and installs nothing. |
| Invitations (mint, join, wrong and reused refused) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `invite_a`, `join_a`, `invite_b_join_b`, `invitations_are_one_use`); `web/e2e/invite.spec.ts` "the invite card creates a token and shows its join line" | A satellite invite joins once; a spent or made-up token is refused; the card shows the join line with a fresh token. |
| Pairing a browser | `web/e2e/pair.spec.ts` "a pair link pairs the device with a passkey and it appears in Settings"; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/pair_passkey` | The pair link runs the passkey ceremony, the device lands in Settings, and the session survives a reload. |
| Passkey sign-in | `web/e2e/signin.spec.ts` "a paired browser signs back in with one passkey touch" and "a link sign-in offers a passkey, and the saved one signs in after cookies are cleared" | A paired browser signs back in with one touch after its cookies are gone; the signed-out page answers only the boolean (`TestLoginAvailableAnswersTheBoolean`). |
| Session renewal (7 days idle) | `internal/huddle/server/signin_test.go:TestARenewalResendsTheCookie` | A request past the renewal hour re-sends the cookie with its seven-day life; a removed device answers 401 with nothing. |
| Session sync (two sessions, one channel) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `one_channel`, `a_posts_b_reads_once`, `b_posts_a_reads_once`) | Both sessions are tied to one channel and listed as present in it; each reads the other's digest exactly once and hears of the tie on its next prompt. |
| Now list | `web/e2e/now.spec.ts` "the sign-in link opens the hub and the Now list shows the seeded session"; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/now_list` | The list names the live session under its machine, and one waiting on a permission sits in Needs you with its state word. |
| Radar requests count | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_numbers`; `web/e2e/radar.spec.ts` "the requests table lists the seeded rows and the inspector opens one" | Radar answers the fixture's exact request count for today; the table lists each seeded row and the inspector shows one request's numbers. |
| Radar input, output, cache tokens and cost | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_numbers`; `web/e2e/radar.spec.ts` "the overview's Day cards show the seeded totals" | The four token sums match the fixtures exactly, and the day's cost and spend match to the cent. |
| Usage and cost charts | `web/e2e/usage.spec.ts` "the usage view shows the seeded totals and splits the picked day"; `web/e2e/fulltest.spec.ts` "one signed-in walk over TLS covers every tab with no console error" | The tiles carry the seeded token totals, the daily chart draws one line per model with its tooltip, and the Costs series carries the seeded window. |
| Navigation of every view | `web/e2e/radar.spec.ts` "every radar view loads with no console error"; `web/e2e/radar-switch.spec.ts` "switching radar views in one document keeps a single app tree" | All eleven radar views load through the sub-tab row with no console error, and switching views keeps exactly one app tree. |
| Kanban view | `web/e2e/kanban.spec.ts` "the kanban tab lists the board and its three cards in the right columns" | The board names itself and holds each card in the column its folder put it in; the Holder filter narrows the board and Clear all restores it. |
| Layout at phone width | `web/e2e/layout.spec.ts` "every view lays out clean: dark phone" and "every view lays out clean: light phone" | Every tab and radar view opens at 390x844 with no console error, no sideways page scroll and no overlapping Kanban toolbar controls. |
| Radar settings writes from a paired device | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_writes` | A phone paired over TLS saves a budget through the Radar tab under the hub's own Origin; a foreign Origin gets 403. |

## Needs a human

- `claude rc` against a real Claude login: the suites feed hooks canned stdin and never run a live session.
- A public certificate: `huddle cert` with a real DNS provider token; the suites mint their own CA instead.
- Installing the local CA root into the OS trust store (`huddle trust`): tests run against a fake store; the
  password prompt needs the owner.
- Face ID / Touch ID on real hardware: the suites use a virtual authenticator and a soft key.
- TOTP sign-in with a real authenticator app on a phone: the suites compute codes from the secret.

## Live run

1. On the hub machine, read the home's `config.toml`: `role = "hub"`, the hub's `host` set to its name,
   `listen` holding 8443.
2. Install the pair you hold (mkcert, a company CA): `huddle cert --import cert.pem key.pem`.
3. Start the server (`huddle serve`) and wait for its `hub listener on https://...:8443` line.
4. Open https://hub.raouf.test:8443 in a browser and sign in with the owner link or a paired passkey.
5. Walk each view: Now, Channels, every Radar view, Kanban, Settings.
6. Note anything off — a view that errors, a number that looks wrong — for the Report below.

## Report

Filled in by the lead after the live run.
