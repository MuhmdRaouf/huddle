# The lead: turning a goal into tickets and running the workers

Contents: Plan, Write the wave, Dispatch, Review, Keep it moving.

## Plan

1. Write the goal as one sentence and its done-when as checkable lines.
2. Read the code the goal touches. Tickets written from memory carry wrong paths.
3. List the pieces of work. Split until each is one concern and at most size M. A piece that needs a design
   decision gets a `decision` ticket first; its result goes into the dependent tickets' Implementation details.
4. Order by dependency. Draw the chain; the first wave is every ticket with no open dependency.
5. Decide the shared contract once (formats, names, messages, file layout) and write it where every ticket can
   point: a spec file in the repo, named in each ticket's Context with the section to read.

## Write the wave

For each ticket, in dependency order: `kanban new`, fill every section (`tickets.md`), run the blind test, then
`kanban lint`. Promote only the first wave; the rest wait in backlog so the ready column shows real choices.

## Dispatch

One worker per ticket, each in its own git worktree and branch, so parallel work never collides:

```sh
git worktree add -b work/<ID> <scratch>/wt/<ID> <base branch>
kanban pull --actor <worker> --id <ID> --branch work/<ID> --worktree <scratch>/wt/<ID>
```

The worker's prompt carries only what the ticket cannot: the worktree path, the actor name, and how to report.

```
You are worker <actor>. Ticket: <ID>.
Worktree (work only here): <path> (branch work/<ID>).
Read the whole ticket first: kanban show <ID>. It holds the plan, the decisions, the proof.
Follow the kanban skill's worker loop. Do not commit; stage with git add -A when done.
Final report: TICKET / STATUS / FILES / RED (the failing test output) / GREEN (each Verification command with its
exit code) / NOTES (anything in the ticket that was wrong or unclear).
```

Tickets whose files overlap do not run at the same time; make one depend on the other.

## Review

When a worker reports:

1. Read the diff against the ticket's Acceptance and Out of scope, not against your memory.
2. Run the Verification commands yourself, or hand the ticket to a verifier agent.
3. Accept: `kanban test` then `kanban done` (as a different actor from the worker), merge the branch, remove the
   worktree.
4. Return: `kanban reject ID --actor <you> --note '<what to change>'` and send the worker the note.
5. The report's NOTES are feedback on your ticket. When a ticket was wrong, fix the next tickets that share the
   mistake before anyone pulls them.

## Keep it moving

- After every wave, `kanban status`: promote the tickets whose dependencies are now done.
- A ticket in progress twice as long as its estimate: ask the worker for its state, or block it with the cause.
- A broken main stops the line before anyone builds on it.
- Keep the live board open (`/huddle:kanban-start`) and, when the user wants a link, export and publish it.
