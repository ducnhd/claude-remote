package main

import "io"

// ptySession is the claude process attached to a pseudo-terminal: a Unix pty
// (pty_unix.go) or a Windows ConPTY (pty_windows.go).
//
// Read returns the terminal's output and Write feeds it keystrokes. Wait
// blocks until the process exits and must be called exactly once; Kill and
// Close may be called at any time, any number of times. Close tears the
// terminal down, which unblocks a pending Read.
type ptySession interface {
	io.ReadWriteCloser
	Resize(rows, cols uint16) error
	Wait() error
	Kill() error
}
