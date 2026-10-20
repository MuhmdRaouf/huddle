---
name: kanban
description: Run the project's file-based kanban board in .agents/kanban with the kanban command - write tickets a worker can finish without asking, pull, review, test and finish them through the gates, block and expedite, answer status and standup questions, and keep the live board open. Use whenever the user or a teammate mentions the kanban, the board, a ticket or card on it, .agents/kanban, /huddle:* commands, planning work into tickets, "what should I work on next", "what is in flight", who holds what, a blocked or stuck ticket, expedite or urgent work, review or testing gates, stop-the-line, or publishing the board. Not for GitHub, GitLab, Jira, Linear, Trello, Asana or Notion boards, pull requests, CI stages, queue backlogs or dashboards.
---

# Kanban: the board in .agents/kanban

The board is a folder. Each column is a subfolder; each ticket is one Markdown file; a ticket's column is the
folder it sits in. The `kanban` command moves tickets and enforces the gates. A local server watches the folders and
shows every change on the live board within half a second.

```text
.agents/kanban/
  backlog/  ready/  in-progress/  review/  testing/  blocked/  done/
  README.md   LOG   EPOCH   STOP-LINE (only while the line is stopped)
```

## First move in any session

1. Run `kanban status`. It prints the epoch, any stopped line, the expedite lane and a count per column.
2. Exit 4 means there is no board here. Offer `/huddle:kanban-init` (or `kanban init`); never create the folders by hand.
3. `kanban: command not found`: the session hook has not put `kanban` on PATH yet (its first run downloads the
   binary in the background). Ask the user to run `/huddle:status`: it runs the plugin's launcher directly and
   downloads the binary if it is missing. `kanban` is on PATH from the next session. Do not search for the binary
   or install anything.

## Which command when

| You want to | Run | Detail |
|---|---|---|
| See where things stand | `kanban status`, `kanban list [column]` | §Status and rundowns |
| Read one ticket | `kanban show ID` | the whole file, with its column and badges |
| Write tickets | `kanban new ...`, then edit the body, `kanban lint` | `references/tickets.md` |
| Make a ticket workable | `kanban promote ID --actor A` | refused until the ticket meets the bar |
| Take work | `kanban pull --actor A [--id ID --branch B --worktree W]` | §The worker loop |
| Record progress or evidence | `kanban log ID --actor A --note '...'` | appends to the ticket's `## Log` |
| Hand work to review | `kanban review ID --actor A --note '...'` | fix, feature, test tickets |
| Approve after re-running the proof | `kanban test ID --actor V --note '<commands + exit codes>'` | a different actor |
| Send it back | `kanban reject ID --actor V --note '<what is wrong>'` | a different actor |
| Finish | `kanban done ID --actor V --note '<result>'` | `references/flow.md` §Gates |
| Park it | `kanban block ID --actor A --note '<cause>' --on X --until '<condition>'` | `references/flow.md` §Block |
| Urgent work | `kanban new ID ... --expedite --body-file F` | `references/flow.md` §Expedite |
| Main is broken, everyone stop | `kanban stop-line --actor A --note '...'`, later `resume-line` | `references/flow.md` §Stop the line |
| Plan a goal into tickets, run workers | | `references/lead.md` |
| Open the live board | `kanban start` | §The live board |
| Share a snapshot link | `kanban export --out FILE`, then the Artifact tool | §The live board |

Every command that changes the board takes `--actor`, a short id that is yours alone (`lead`, `w1`, `rev`). Exit
codes: 0 ok; 1 refused (the message says why and what to do next: read it, do not retry blindly); 2 usage (run
`kanban <cmd> --help`); 4 no board; 6 no server running.

## The rule that matters most: move tickets only with `kanban`

Never `mv`, `git mv` or edit a ticket into another folder. The command checks the gates and writes the audit trail.
A hand move is not refused, but the watcher flags the ticket `moved by hand` on the board and in LOG, and the lead
will ask why. Editing a ticket's text in place (filling sections, fixing a typo) is fine; changing its column is not.

## The worker loop

You were handed one ticket. Take it end to end:

1. `kanban epoch` and note the number. Check it again before every commit; if it changed, run `kanban status` and
   read STOP-LINE before going on.
2. `kanban pull --actor <you> --id <ID> --branch <B> --worktree <W>`. One ticket in progress per actor.
3. `kanban show <ID>` and read the whole ticket: Description, Context (read every file it names), Implementation
   plan, Implementation details, Acceptance, Verification, Out of scope. The ticket was written so you make no
   design decision; follow it.
4. Tests first: write the failing test the plan names, run it, keep the failing output.
5. Implement, step by step as the plan numbers them. Stay inside Out of scope's fence.
6. Run every Verification command. Record each with its exit code:
   `kanban log <ID> --actor <you> --note 'go test ./x -count=1: exit 0; RED was: <one line>'`.
7. `kanban review <ID> --actor <you> --note '<files changed; what to look at; anything the ticket got wrong>'`.
   Docs, ops, decision and chore tickets skip review: `kanban done` directly.

When the ticket is wrong or incomplete (a path that does not exist, a step that cannot work), do not improvise a
redesign. Do the part that is clear, log what is wrong with `kanban log`, and say so in the review note or your
report. When you cannot go on at all, `kanban block` it with the cause, what it waits on and the condition that
unblocks it.

Never weaken, skip or delete a test to get green. Never write evidence you did not see; an exit code is the code
the command returned.

## The verifier

A verifier is a different agent from the author and every holder (the command refuses them). Re-run every
Verification command in a clean state, then:

- all pass: `kanban test <ID> --actor <you> --note '<each command>: exit 0'` (the note needs an exit code or a
  pass count), then `kanban done <ID> --actor <you> --note '<result>'`;
- anything fails or the diff breaks Acceptance: `kanban reject <ID> --actor <you> --note '<what failed, the
  command and its output line>'`.

## Writing tickets

Read `references/tickets.md` before writing any. The bar, in one line: a worker who has never seen this repository
finishes the ticket from the ticket alone, with no question and no design decision. `kanban new` writes the
skeleton; you fill every section; `kanban lint` and `promote` refuse a ticket that still has a gap. On a done
ticket the field limits only warn, because a done ticket is never edited.

## Status and rundowns

Build a rundown from `kanban status` and `kanban list`; open single tickets only when needed. About 150 words:

1. `Look at first: <ID>, <why>`. An open expedite ticket is always this line.
2. One line per ticket in ready, in-progress, review or testing: id, priority, column, what it is in plain words,
   who holds it.
3. `Waiting:` lines: each blocked ticket with its cause and unblock condition, and any in-progress ticket whose
   dependency has not started.

A rundown is read-only: no moves, no edits. Done tickets appear only as a count. A worked example:
`references/rundown.md`.

## The live board

- The huddle hub serves the board: the session hook names its page when the project has a board; `kanban start`
  prints the same address by hand (starting the hub when the huddle home is set up), `kanban open` opens the page
  again, and `/huddle:kanban-refresh` rescans now and reloads open pages. `/huddle:stop` stops the hub the boards
  are served by.
- The page shows each column, an expedite lane, badges (priority, type, size, blocked, holder, waits on, moved by
  hand, incomplete), tags, and a drawer with the whole ticket. It works on a phone.
- To share it beyond this machine: `kanban export --out <scratchpad>/kanban-board.html`, then publish that file
  with the Artifact tool (first publish: note the URL; later publishes pass the same file path and URL). Tickets
  become visible to whoever opens the link, so keep secrets, tokens and personal data out of tickets.

## Never

- Move a ticket by hand, or edit a done ticket.
- Hold two tickets in progress, or pull past an expedite ticket that is waiting (the command refuses; do not
  `--force` it unless the lead says so).
- Put secrets, keys or personal data in a ticket or a note.
- Approve your own work: `test` and `done` on an implementation ticket belong to someone else.
