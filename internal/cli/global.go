package cli

import (
	"context"
	"fmt"
	"io"
	"time"
)

// runGlobalCommand joins or leaves the opt-in global board. Joining makes the
// account's display name and usage totals visible to every signed-in user, so
// the command says so and nothing else ever joins on the user's behalf.
func runGlobalCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) != 1 || args[0] != "join" && args[0] != "leave" {
		fmt.Fprintln(stderr, "global requires join or leave")
		return 2
	}
	manager, ok := configuredGroupManager(stderr, deps)
	if !ok {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	joining := args[0] == "join"
	if err := manager.SetGlobalParticipation(ctx, joining); err != nil {
		fmt.Fprintln(stderr, "could not update the global board; sign in and check the API configuration")
		return 1
	}
	if joining {
		fmt.Fprintln(stdout, "Joined the global board. Your display name and usage totals are now visible to every signed-in tokenholic user.")
		fmt.Fprintln(stdout, "Run `tokenholic global leave` to stop being listed.")
		return 0
	}
	fmt.Fprintln(stdout, "Left the global board. You are no longer listed on it; your private groups are unchanged.")
	return 0
}
