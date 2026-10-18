# Writing tickets a worker can finish blind

Contents: The bar, Create one, Front matter, Each section, A complete example, The blind test, Mistakes that cost
a round trip.

## The bar

A worker who has never seen this repository, holding only the ticket, finishes it end to end:

- with no question to anyone;
- with no design decision of their own (every choice that matters was made while planning and is written down);
- with proof anyone can re-run (commands and the exit code each must return).

A ticket that needs its author in the room is not ready. `kanban lint` checks the shape; you check the substance.

## Create one

```sh
kanban new AUTH-RESET --actor lead --type feature --priority P1 --est M \
  --title "auth: emailed password reset token with a 30 minute expiry" \
  --cluster auth --tag security --tag email \
  --summary "Adds a one-time password reset token, mailed to the user and stored with a 30 minute expiry. \
Without it, locked-out users need support to get back in. Done when a token redeems once and an expired one gets 410." \
  --depends AUTH-BASE
```

Then open `.agents/kanban/backlog/AUTH-RESET.md` and replace every `<!-- fill: ... -->` comment. Run `kanban lint`
until it names no failure for the ticket, then `kanban promote AUTH-RESET --actor lead`.

Create tickets in dependency order: `--depends` must name tickets already on the board.

## Front matter

| Field | How to write it |
|---|---|
| `id` | `AREA-NAME`, capitals, digits and dashes; stable forever. The area groups related tickets (`AUTH-`, `KBN-`). |
| `title` | At most 90 characters. Area, then the change, then where: `auth: emailed reset token, 30 minute expiry`. Not "fix auth". |
| `summary` | 80 to 600 characters, 2 to 4 sentences: what changes, why, and `Done when ...`. The board card shows it; a reader of the collapsed card should know what and why. |
| `type` | `fix`, `feature`, `test` go through review and testing. `docs`, `ops`, `decision`, `chore` go straight to done. |
| `priority` | `P0` drop everything, `P1` this wave, `P2` next, `P3` someday. |
| `est` | `S` under an hour of agent work, `M` a few hours, `L` a day. Split anything bigger. |
| `cluster` | One or two lowercase words; the board groups by it. |
| `tags` | Up to 8 lowercase, dash-joined labels for filtering (`security`, `macos`, `ui`). |
| `depends_on` | Ticket ids this one waits on; plain text for outside things (`"the vendor's API key"`). |

## Each section

### Description

What changes and why, in a short paragraph of plain prose. Name the user-visible effect. End with one line:
`Done when: <an objective condition>`. "Done when the reset endpoint works" is not objective; "Done when a token
redeems once, a second use gets 409 and an expired one gets 410" is.

### Context

Everything the worker must read or know before touching code:

- exact paths of the files to read, and why each (`internal/auth/session.go`: the session table this extends);
- tickets this builds on, and what each left behind;
- decisions already made and binding (`tokens are stored hashed with sha256, never plain`);
- constraints (`no new dependency`, `paths with spaces must work`, `the public API stays the same`);
- prior art to copy (`the email-verify flow in internal/auth/verify.go is the pattern`).

### Implementation plan

Numbered steps. Each step is one bounded action on named paths. Tests come first. A good step:
`3. Write internal/auth/reset_test.go: the four cases in Acceptance as table rows. Run it; it must fail.`
A bad step: `3. Add tests.`

Keep steps in the order they happen. Six to twelve steps is typical for an M ticket.

### Implementation details

The decisions made while planning, written so the worker does not make them again:

- exact signatures and types, as code blocks;
- data formats, file layouts, JSON shapes, column names;
- every user-facing message, word for word;
- each edge case and what to do in it (empty input, a missing file, a race, a read-only directory);
- one worked example of input and output where the format is not obvious.

If you find yourself writing "choose a suitable ...", the ticket is not ready: choose it here.

### Acceptance

`- [ ]` boxes, each checkable by someone who did not do the work, with no judgement call:
`- [ ] A second redeem of the same token answers 409 with {"error":"token used"}`.
Not: `- [ ] Code is clean`.

### Verification

Fenced commands, each with the exit code it must return, in the order to run them:

```sh
go test ./internal/auth -run TestReset -count=1   # exit 0
go vet ./...                                      # exit 0
```

These are what the worker logs and the verifier re-runs. Include the project's standard checks.

### Out of scope

What not to touch, and where that work lives instead: `The reset email's HTML template (AUTH-MAIL-UI)`. This is the
fence that keeps a blind worker from wandering.

### Log

Left empty when writing. Workers append evidence and findings with `kanban log`; flow notes land here too.

## A complete example

````markdown
---
id: AUTH-RESET
title: "auth: emailed password reset token with a 30 minute expiry"
cluster: auth
tags: [security, email]
summary: >-
  Adds a one-time password reset token, mailed to the user and stored with a 30 minute expiry. Without it,
  locked-out users need support to get back in. Done when a token redeems once and an expired one gets 410.
type: feature
priority: P1
est: M
depends_on: [AUTH-BASE]
---

# AUTH-RESET: auth: emailed password reset token with a 30 minute expiry

## Description
Users who forget their password can ask for a reset link by email and set a new password with it, without
support. The token in the link works once and for 30 minutes.

Done when: POST /auth/reset mails a link, a token redeems once, a second use gets 409, an expired one gets 410.

## Context
- internal/auth/session.go: the users and sessions tables AUTH-BASE added; reset rows live beside them.
- internal/auth/verify.go: the email-verify flow; copy its token generation and mail call.
- internal/mail/mail.go: Send(ctx, to, subject, body) is the only way to send mail; tests use mail.Fake.
- Binding: tokens are stored as sha256 hex, never plain. No new dependency.

## Implementation plan
1. Write migrations/0002_reset_tokens.sql (table in Implementation details). Run `make migrate-test`; it fails.
2. Write internal/auth/reset_test.go: the rows in Acceptance, with mail.Fake and a fixed clock. Run; it fails.
3. Add internal/auth/reset.go with Issue and Redeem (signatures below).
4. Register POST /auth/reset and POST /auth/reset/confirm in internal/http/routes.go.
5. Run Verification; log each exit code.

## Implementation details
```go
func Issue(ctx context.Context, db *sql.DB, m mail.Sender, now time.Time, email string) error
func Redeem(ctx context.Context, db *sql.DB, now time.Time, token, newPassword string) error
var ErrTokenUsed = errors.New("token used")       // HTTP 409 {"error":"token used"}
var ErrTokenExpired = errors.New("token expired") // HTTP 410 {"error":"token expired"}
```
- Table reset_tokens(id integer pk, user_id integer not null, token_sha256 text unique not null,
  expires_at timestamp not null, used_at timestamp null).
- Token: 32 random bytes, base64url without padding. Expiry: now + 30 minutes.
- An unknown email answers 202 like a known one (no account probing), and sends nothing.
- Subject: "Reset your password". Body: "Use this link within 30 minutes: <base>/reset?t=<token>".

## Acceptance
- [ ] POST /auth/reset with a known email answers 202 and mail.Fake holds one message with the link.
- [ ] POST /auth/reset with an unknown email answers 202 and mail.Fake holds nothing.
- [ ] Confirming a fresh token sets the password; a second confirm answers 409 {"error":"token used"}.
- [ ] Confirming 31 minutes later answers 410 {"error":"token expired"}.
- [ ] The database never holds the plain token.

## Verification
```sh
make migrate-test                         # exit 0
go test ./internal/auth -count=1          # exit 0
go vet ./... && test -z "$(gofmt -l .)"  # exit 0
```

## Out of scope
- The reset page's HTML (AUTH-MAIL-UI). Rate limiting (AUTH-RATE).

## Log
````

## The blind test

Before `promote`, read the ticket as the worker will, with nothing else open, and answer:

1. Could I start step 1 right now, knowing which file to open?
2. Is there any choice left (a name, a format, a message, a library, what to do on an error)? Make it in the ticket.
3. Does every Acceptance box map to a test or a Verification command?
4. Would two different workers produce the same behaviour from this ticket?
5. Is the fence (Out of scope) clear enough that nobody touches the neighbouring ticket's files?

Any "no" is a gap. Fill it, then promote.

## Mistakes that cost a round trip

- A path that does not exist, or the right file named by a vague phrase ("the auth module").
- "Follow the existing pattern" without naming the file that holds it.
- Messages described ("an error saying it expired") instead of written (`token expired`).
- Verification that only says "tests pass": name the commands.
- Two concerns in one ticket (a feature and a refactor). Split them; link with `depends_on`.
- An L ticket. Split it until each piece is M or smaller.
