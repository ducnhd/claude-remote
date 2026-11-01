//go:build !windows

package main

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// unixPTY is a process attached to a pty master; the embedded file carries
// Read, Write and Close.
type unixPTY struct {
	*os.File
	cmd *exec.Cmd
}

func startPTY(name string, args []string, dir string, env []string) (ptySession, error) {
	c := exec.Command(name, args...)
	c.Env = env
	if dir != "" {
		c.Dir = dir
	}
	f, err := pty.Start(c)
	if err != nil {
		return nil, err
	}
	return &unixPTY{File: f, cmd: c}, nil
}

func (p *unixPTY) Resize(rows, cols uint16) error {
	return pty.Setsize(p.File, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *unixPTY) Wait() error { return p.cmd.Wait() }

func (p *unixPTY) Kill() error { return p.cmd.Process.Kill() }
