//go:build windows

package main

import (
	"fmt"
	"testing"
)

// Stand-in commands for the claude process, one set per OS. ping is the
// usual cmd.exe sleep: timeout refuses to run without a real keyboard.

// echoCommand prints its arguments.
func echoCommand() (string, []string) { return "cmd.exe", []string{"/c", "echo"} }

// catCommand echoes its input back: cmd.exe's own line editor does.
func catCommand() (string, []string) { return "cmd.exe", nil }

// countCommand prints line-1 … line-n, then idles for a second.
func countCommand(n int) (string, []string) {
	return "cmd.exe", []string{"/c", fmt.Sprintf("(for /L %%i in (1,1,%d) do @echo line-%%i) & ping -n 2 127.0.0.1 >nul", n)}
}

// sleepCommand runs for secs seconds.
func sleepCommand(secs int) (string, []string) {
	return "cmd.exe", []string{"/c", fmt.Sprintf("ping -n %d 127.0.0.1 >nul", secs+1)}
}

// appendCommand appends word and a newline to file.
func appendCommand(file, word string) []string {
	return []string{"cmd.exe", "/c", "echo " + word + ">>" + file}
}

// skipWithoutSh is for tests whose fakes are shell scripts.
func skipWithoutSh(t *testing.T) {
	t.Helper()
	t.Skip("fake is a shell script; covered on macOS")
}

// ClaudePath stand-ins: stubClaude exits at once, stubCat echoes input.
// where.exe just fails on claude's flags; cmd.exe ignores them and stays
// interactive.
const (
	stubClaude = "where.exe"
	stubCat    = "cmd.exe"
)
