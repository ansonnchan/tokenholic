package autosync

import (
	"fmt"
	"time"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
	"golang.org/x/sys/windows"
)

func platformScheduler(Config) (scheduler, error) {
	root, err := privatefs.ApplicationRoot()
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	return taskScheduler{name: taskName, root: root, userID: user.User.Sid.String(), now: time.Now, run: runCommand}, nil
}
