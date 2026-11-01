//go:build !windows

package main

import (
	"fmt"
	"testing"
)

// Stand-in commands for the claude process, one set per OS.

// echoCommand prints its arguments.
func echoCommand() (string, []string) { return "echo", nil }

// catCommand echoes its input back.
func catCommand() (string, []string) { return "cat", nil }

// countCommand prints line-1 … line-n, then idles for a second.
func countCommand(n int) (string, []string) {
	return "sh", []string{"-c", fmt.Sprintf("for i in $(seq 1 %d); do echo line-$i; done; sleep 1", n)}
}

// sleepCommand runs for secs seconds.
func sleepCommand(secs int) (string, []string) {
	return "sh", []string{"-c", fmt.Sprintf("sleep %d", secs)}
}

// appendCommand appends word and a newline to file.
func appendCommand(file, word string) []string {
	return []string{"/bin/sh", "-c", "echo " + word + " >> " + file}
}

// skipWithoutSh is for tests whose fakes are shell scripts.
func skipWithoutSh(t *testing.T) { t.Helper() }

// ClaudePath stand-ins: stubClaude exits at once, stubCat echoes input.
const (
	stubClaude = "echo"
	stubCat    = "cat"
)
