package cli

import (
	"fmt"
	"io"
)

// usage is the line every unknown command prints.
const usage = "usage: kanban <command> [flags]; run kanban help for the list"

// topHelp is the command list `kanban help` prints.
const topHelp = `kanban: a file-based kanban board. One ticket is one Markdown file; its column
is its folder. Move tickets only with kanban commands.

usage: kanban <command> [flags]

  init [--from DIR]      create .agents/kanban, or migrate a reference board
  init --db              create the board inside one kanban.db file, no folders
  new ID                 write a full ticket into backlog, or ready under the ticket bar
  promote ID             move a backlog ticket to ready, under the ticket bar
  pull                   claim the best ready ticket, or --id one
  review ID              submit an in-progress ticket to review
  reject ID              return a review ticket to in-progress with the verdict
  test ID                move a review ticket to testing under an evidence note
  done ID                finish a ticket (implementation tickets from testing only)
  block ID               park a ticket with its cause and unblock condition
  unblock ID             return a blocked ticket to ready
  set ID key=value ...   edit a ticket's descriptive fields
  move ID COLUMN         the lead's repair: any column to any column, always logged
  log ID                 append one entry to a ticket's Log section
  feature close|reopen FEATURE  close a merged feature (its cards leave every column and count) or reopen it
  list [COLUMN]          one line per ticket: badges and dependency states
  show ID                one ticket whole: its fields and its body
  status                 epoch, stop-line, the expedite lane, per-column counts
  check [ID]             the stop-line state, plus one ticket's column, type and gates
  lint                   every ticket against the bar (sections, deps, not UTF-8)
  epoch                  print the coordination epoch
  stop-line              halt the board: epoch +1 and the STOP-LINE record
  resume-line            clear the stop-line after verified green
  migrate-db             turn the folder board into one kanban.db, folders kept aside
  start [--port N]        ensure the board's server and print its address
  stop                    stop the board's server
  open                    print the board's address and open it in the browser
  refresh                 rescan the board now through the running server
  export --out FILE       write the board as one self-contained HTML page
  export-md --out DIR     write the board back out as Markdown folders
  serve [--port N]        the foreground server kanban start runs
  hook session-start      the Claude Code session hook: ensure the server, name the board

run kanban <command> --help for one command's flags.
`

// printTopHelp writes the command list.
func printTopHelp(w io.Writer) {
	fmt.Fprint(w, topHelp)
}

// printCommandHelp writes one command's help: its usage form, its description,
// its positionals and its flags (in name order, then --board, which every
// board command takes).
func printCommandHelp(w io.Writer, cmd *command) {
	fmt.Fprintf(w, "usage: kanban %s [flags]\n%s\n\n", cmd.use, cmd.short)
	type row struct{ head, help string }
	var rows []row
	for _, p := range cmd.positionals {
		if p.help != "" {
			rows = append(rows, row{p.name, p.help})
		}
	}
	for _, def := range cmd.sortedDefs() {
		head := "--" + def.name
		if def.kind != kindBool {
			head += " " + def.kind
		}
		if def.required {
			head += " (required)"
		}
		rows = append(rows, row{head, def.help})
	}
	rows = append(rows, row{"--board string", boardHelp})
	width := 0
	for _, r := range rows {
		if len(r.head) > width {
			width = len(r.head)
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %s\n", width, r.head, r.help)
	}
}
