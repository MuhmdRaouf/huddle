---
description: Write the board as one self-contained HTML page for sharing
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban export $ARGUMENTS 2>&1`

Show the output above to the user as it is, with no preamble or recap.

Run it with `--out` naming a writable path outside the board: `kanban-board.html` under the directory you use
for scratch files this session, else the working directory. Publish the written file with the Artifact tool
when the user wants a link, and say that everyone who opens the link can read every ticket.
