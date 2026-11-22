package cli

import "golang.org/x/sys/windows"

// detachConsole gives up the console window Windows opens for a console
// program that Task Scheduler starts in the user's session. The window may
// still appear for a moment before this runs.
func detachConsole() {
	_, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()
}
