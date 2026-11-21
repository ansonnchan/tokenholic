package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

func runGroupCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "group requires list, create, join, invite, or leave")
		return 2
	}
	switch args[0] {
	case "list", "create", "join", "invite", "leave":
	default:
		fmt.Fprintf(stderr, "unknown group command %q\n", args[0])
		return 2
	}
	manager, ok := configuredGroupManager(stderr, deps)
	if !ok {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "group list accepts no arguments")
			return 2
		}
		groups, err := manager.List(ctx)
		if err != nil {
			return groupCommandFailure(stderr)
		}
		if len(groups) == 0 {
			fmt.Fprintln(stdout, "No private groups yet.")
			return 0
		}
		for _, group := range groups {
			fmt.Fprintf(stdout, "%s · %s · %s · id %s\n", group.Name, group.Role, group.Timezone, group.ID)
		}
		return 0
	case "create":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintln(stderr, "group create requires one name")
			return 2
		}
		group, err := manager.Create(ctx, args[1])
		if err != nil {
			return groupCommandFailure(stderr)
		}
		fmt.Fprintf(stdout, "Created private group %s (%s).\n", group.Name, group.Timezone)
		return 0
	case "join":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "group join requires one invite code")
			return 2
		}
		group, err := manager.Join(ctx, args[1])
		if err != nil {
			return groupCommandFailure(stderr)
		}
		fmt.Fprintf(stdout, "Joined private group %s.\n", group.Name)
		return 0
	case "invite":
		flags := flag.NewFlagSet("tokenholic group invite", flag.ContinueOnError)
		flags.SetOutput(stderr)
		lifetime := flags.Duration("expires", 24*time.Hour, "invite lifetime")
		uses := flags.Int("uses", 10, "maximum invite uses")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if flags.NArg() != 1 || *lifetime < 5*time.Minute || *lifetime > 30*24*time.Hour || *uses < 1 || *uses > 100 {
			fmt.Fprintln(stderr, "group invite requires one group plus --expires 5m..720h and --uses 1..100")
			return 2
		}
		invite, err := manager.Invite(ctx, flags.Arg(0), *lifetime, *uses)
		if err != nil {
			return groupCommandFailure(stderr)
		}
		fmt.Fprintf(stdout, "Invite code: %s\nExpires: %s · uses: %d\n", invite.Code, invite.ExpiresAt.UTC().Format(time.RFC3339), invite.MaxUses)
		return 0
	case "leave":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "group leave requires one group name or ID")
			return 2
		}
		if err := manager.Leave(ctx, args[1]); err != nil {
			return groupCommandFailure(stderr)
		}
		fmt.Fprintf(stdout, "Left private group %s.\n", args[1])
		return 0
	}
	return 2
}

func configuredGroupManager(stderr io.Writer, deps dependencies) (groupManager, bool) {
	apiURL := strings.TrimSpace(deps.getenv("TOKENHOLIC_API_URL"))
	if apiURL == "" {
		fmt.Fprintln(stderr, "TOKENHOLIC_API_URL is required for group and global board commands")
		return nil, false
	}
	if deps.groups == nil {
		fmt.Fprintln(stderr, "group support is unavailable")
		return nil, false
	}
	manager, err := deps.groups(apiURL)
	if err != nil {
		fmt.Fprintln(stderr, "TOKENHOLIC_API_URL is invalid")
		return nil, false
	}
	return manager, true
}

func groupCommandFailure(stderr io.Writer) int {
	fmt.Fprintln(stderr, "could not complete the group command; sign in and check the API configuration")
	return 1
}
