package cli

import (
	"context"
	"fmt"
	"io"
	"time"
)

// runProfileCommand shows the account's visibility or makes its profile
// public or private. Publishing is always an explicit command.
func runProfileCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "public" && args[0] != "private" {
		fmt.Fprintln(stderr, "profile accepts no argument, or public or private")
		return 2
	}
	manager, ok := configuredGroupManager(stderr, deps)
	if !ok {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if len(args) == 1 {
		public := args[0] == "public"
		if err := manager.SetProfileVisibility(ctx, public); err != nil {
			return profileCommandFailure(stderr)
		}
		if public {
			fmt.Fprintln(stdout, "Your profile is public. Anyone can see your GitHub login, avatar, and usage totals.")
			fmt.Fprintln(stdout, "Run `tokenholic profile private` to hide it again.")
			return 0
		}
		fmt.Fprintln(stdout, "Your profile is private. It no longer appears publicly or on public group boards.")
		return 0
	}
	profile, err := manager.Profile(ctx)
	if err != nil {
		return profileCommandFailure(stderr)
	}
	global := "not listed"
	if profile.GlobalOptIn {
		global = "listed"
	}
	fmt.Fprintf(stdout, "@%s\nProfile: %s\nGlobal board: %s\n", profile.Login, profile.Visibility, global)
	if profile.Visibility == "public" {
		fmt.Fprintf(stdout, "Public profile: /v1/public/users/%s on the configured API\n", profile.Login)
	}
	return 0
}

func profileCommandFailure(stderr io.Writer) int {
	fmt.Fprintln(stderr, "could not complete the profile command; sign in and check the API configuration")
	return 1
}
