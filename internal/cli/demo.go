package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ansonnchan/tokenholic/internal/demo"
	"github.com/ansonnchan/tokenholic/internal/tui"
)

// runDemoCommand opens the interface on invented usage and boards. It reads
// no agent records, opens no database, signs nobody in, and contacts no
// server, so it is safe to show or record on any machine.
func runDemoCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "demo accepts no arguments")
		return 2
	}
	if deps.demo == nil {
		fmt.Fprintln(stderr, "could not start the demo")
		return 1
	}
	if err := deps.demo(context.Background(), deps.input, stdout, displayFromEnvironment(deps.getenv)); err != nil {
		fmt.Fprintln(stderr, "could not start the demo")
		return 1
	}
	fmt.Fprintln(stdout, "That was the demo: every name and number in it is invented. Run `tokenholic` to see your own usage.")
	return 0
}

// launchDemo runs the TUI over the demo's in-memory data. The look starts
// light, or as TOKENHOLIC_THEME says, and a switch is not saved: the demo
// changes nothing on the machine.
func launchDemo(ctx context.Context, input io.Reader, output io.Writer, display displayOptions) error {
	config := tui.Config{
		NoColor: display.noColor, ASCII: display.ascii,
		Multiplayer: &demo.Boards{}, Notice: demo.Notice,
	}
	if display.modeSet {
		config.Mode = display.mode
	}
	return tui.Run(ctx, demo.App{}, input, output, config)
}
