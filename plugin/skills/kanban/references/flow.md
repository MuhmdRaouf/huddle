# The flow, its gates and the special cases

Contents: Columns, Gates, Expedite, Block and unblock, Hand moves, Stop the line, Features, Refusals and what to do.

## Columns

backlog -> ready -> in-progress -> review -> testing -> done, with blocked beside them.

| Column | Means | Gets there by |
|---|---|---|
| backlog | written, not yet workable | `new` |
| ready | meets the ticket bar; anyone may pull it | `promote`, `new --ready`, `new --expedite`, `unblock` |
| in-progress | one actor holds it | `pull`, `reject` |
| review | waiting for a different actor to look | `review` |
| testing | proof re-run by a verifier | `test` |
| blocked | parked with a cause and an unblock condition | `block` |
| done | finished with evidence; never edited again | `done` |

## Gates

The route depends on the ticket's `type`:

- `fix`, `feature`, `test` (and any unknown type): in-progress -> `review` (by the holder) -> `test` (by someone who
  is neither the author nor any holder, with an exit code or pass count in the note) -> `done` (again not the
  author or a holder).
- `docs`, `ops`, `decision`, `chore`: `done` from any open column, by anyone.
- `promote`, `new --ready` and `new --expedite` refuse a ticket that fails `kanban lint`; the message lists every
  failure. `--force` overrides and the LOG line records it; use it only when the lead says so.
- `pull` refuses a second ticket in progress for the same actor, a stopped line, and (normally) a ticket whose
  dependency is not done: bare `pull` skips it and names it; `pull --id` takes it anyway.

Working alone? Spawn a subagent as reviewer and verifier. A different actor must be a different agent, not a
different string.

## Expedite

`kanban new ID ... --expedite` writes the ticket straight into ready; it is the highest priority on the board until
done and shows in its own lane. Ready means it must clear the ticket bar like any other: write the body first and pass
it with `--body-file`, even when it is short. Under real pressure the lead may pass `--force` (logged) and fill the
sections right after; nobody else does.

- Bare `pull` takes the expedite ticket before any other (one whose dependency is open is skipped and named).
- `pull --id` of a normal ticket is refused while an expedite ticket with no open dependency waits in ready.
- Workers finish or park what they hold (`block ... --note 'parked for HOT-1' --on HOT-1 --until 'HOT-1 is
  done'`), then pull the expedite ticket. Gates still apply.

## Block and unblock

```sh
kanban block ID --actor A --note '<cause, one sentence>' --on OTHER-ID --on 'the vendor key' \
  --until '<how anyone can tell it is unblocked>'
kanban unblock ID --actor A          # back to ready; anyone pulls it again
```

Give all three of note, on and until; lint fails a blocked ticket without `on` and `until`. The first puller
stays the author; every puller is recorded, and the gates refuse all of them for test and done. When a ticket
blocked from review or testing comes back, the phases it already passed are pass-throughs: a reviewer who is not
the author or a holder runs `review` with a note starting `pass-through:`.

When the cause can be fixed in minutes, fix it instead of blocking.

## Hand moves

The watcher compares each ticket's folder with the last one it saw. A ticket that changed folder with no `kanban`
command behind it is marked `moved by hand` (front matter `moved_by_hand`, a LOG line, a badge on the board). It is
not moved back. The next `kanban` command on the ticket clears the mark. When you see one:

1. `kanban show ID` and read LOG for the move.
2. If the move skipped a gate, move it back with `kanban move ID <column> --actor lead --note '<why>'` and run
   the missing step properly.

## Stop the line

When main is broken or a shared assumption turns out wrong, anyone runs
`kanban stop-line --actor A --note '<what is broken>'`. The epoch goes up by one and `pull` is refused until
`kanban resume-line --actor A`. Workers check `kanban epoch` before edits and commits; a changed epoch means stop,
read `kanban status`, and wait or help fix main.

## Features

A feature is a cluster: the group name its tickets share. When a feature ships, the lead closes it. Closing moves
no ticket and writes no ticket file; the cards stay in their folders but leave every column list and count, and
`kanban show` says `Feature closed: <feature>` on a closed feature's card.

```sh
kanban feature close swr --actor lead     # the swr cards leave every column and count
kanban feature reopen swr --actor lead    # the swr cards read as they did
```

## Refusals and what to do

Exit 1 always names the ticket's real column and the next step. Common ones:

| Message starts with | Do |
|---|---|
| `... is in <column>/, not ...` | You named the wrong state. Run `kanban show ID` and use the command for that column. |
| `expedite <ID> is waiting` | Pull the expedite ticket first. |
| `... already holds ...` | Finish or block the ticket you hold. |
| `board stopped (epoch N) by ...` | Read `kanban status`; wait for `resume-line`. |
| `... is the author or holder` | Ask a different agent to test or finish it. |
| `<ID>: under the ticket bar: ...` | Fill the listed sections, run `kanban lint`, then retry. |
| `note needs evidence` | Put the commands and their exit codes in the note. |
| `no feature <name> on this board` | No card carries that cluster. Check the spelling against a card's `cluster` field. |
| `feature <name> is not closed` | Nothing to reopen; the feature is already open. |
