package cli

import (
	"fmt"
	"io"
)

// usageText lists every command. docs/commands.md is the full reference; a
// test fails when a dispatched command is missing from this list.
const usageText = `tokenholic: a terminal leaderboard for your coding-agent usage

Usage:
  tokenholic              open the terminal interface
  tokenholic <command>

On this machine, no account needed:
  demo                    open the interface on invented data
  doctor                  check each usage source and local storage
  show-data               print the exact upload payload; nothing is sent
  share [--period P]      draft a post of your totals: today, week, 30d, all
  ccusage on|off|status   the pinned ccusage that reads the other agents

Account and sync:
  claim                   sign in with GitHub, then upload
  login                   sign in with GitHub only
  logout
  sync [--dry-run]        collect and upload now
  autosync enable|disable|status
                          hourly background sync; never installed silently
  account delete --confirm DELETE
                          delete the hosted account and its uploaded totals

Groups, the global board, and your profile:
  group list
  group create|leave|public|private <name>
  group invite [--expires 24h] [--uses 10] <name>
  group join <invite code>
  group transfer <name> <new owner>
  group delete --confirm <name> <name>
  global join|leave
  profile [public|private]

Other:
  help, --help            show this
  --version               print the version

Every command is described in full at
https://github.com/ansonnchan/tokenholic/blob/main/docs/commands.md
`

// helpPointer follows a mistake so the next step is one command away.
const helpPointer = "run `tokenholic --help` for the commands"

// asksForHelp reports whether the arguments are a request for the usage text.
func asksForHelp(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "help", "--help", "-help", "-h":
		return true
	}
	return false
}

func printUsage(output io.Writer) {
	fmt.Fprint(output, usageText)
}
