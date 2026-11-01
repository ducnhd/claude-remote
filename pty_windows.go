//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conPTY is a process attached to a Windows pseudo-console (ConPTY, Windows
// 10 1809+). conhost renders the console into VT sequences on out and turns
// what is written to in back into console input, so the rest of the server
// treats it exactly like a Unix pty.
type conPTY struct {
	in  *os.File // keystrokes → console
	out *os.File // console output → us

	mu      sync.Mutex
	console windows.Handle // 0 once closed
	process windows.Handle // 0 once reaped
}

func startPTY(name string, args []string, dir string, env []string) (ptySession, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	app := path
	cmdline := windows.ComposeCommandLine(append([]string{path}, args...))
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".cmd" || ext == ".bat" {
		// npm installs claude as claude.cmd, and batch files only run
		// under cmd.exe. /s strips the outer quotes and keeps the rest.
		app = comSpec()
		cmdline = `cmd.exe /d /s /c "` + cmdline + `"`
	}

	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("output pipe: %w", err)
	}
	var console windows.Handle
	err = windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 25}, inRead, outWrite, 0, &console)
	// conhost holds its own copies of these ends; ours would keep the
	// output pipe open after the console is gone and Read would never end.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	if err != nil {
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	p := &conPTY{
		in:      os.NewFile(uintptr(inWrite), "conpty-in"),
		out:     os.NewFile(uintptr(outRead), "conpty-out"),
		console: console,
	}
	if p.process, err = spawnInConsole(console, app, cmdline, dir, env); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func spawnInConsole(console windows.Handle, app, cmdline, dir string, env []string) (windows.Handle, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		*(*unsafe.Pointer)(unsafe.Pointer(&console)), unsafe.Sizeof(console)); err != nil {
		return 0, fmt.Errorf("attach pseudo-console: %w", err)
	}

	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// When this process's std handles are not a console (a pipe under go
	// test, a log file under the task), the child would be handed them and
	// write there instead of to the pseudo-console. INVALID_HANDLE_VALUE
	// makes it take the console's handles; NULL would leave stdin closed.
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = windows.InvalidHandle
	si.StdOutput = windows.InvalidHandle
	si.StdErr = windows.InvalidHandle
	appPtr, err := windows.UTF16PtrFromString(app)
	if err != nil {
		return 0, err
	}
	cmdPtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return 0, err
	}
	var dirPtr *uint16
	if dir != "" {
		if dirPtr, err = windows.UTF16PtrFromString(dir); err != nil {
			return 0, err
		}
	}
	block, err := envBlock(env)
	if err != nil {
		return 0, err
	}

	var pi windows.ProcessInformation
	if err := windows.CreateProcess(appPtr, cmdPtr, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&block[0], dirPtr, &si.StartupInfo, &pi); err != nil {
		return 0, fmt.Errorf("start %s: %w", app, err)
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

// envBlock encodes env as CreateProcess wants it: NUL-terminated UTF-16
// "k=v" strings followed by one more NUL.
func envBlock(env []string) ([]uint16, error) {
	var block []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("environment variable contains NUL: %q", kv)
		}
		block = append(block, utf16.Encode([]rune(kv))...)
		block = append(block, 0)
	}
	return append(block, 0), nil
}

func comSpec() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
}

func (p *conPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *conPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *conPTY) Resize(rows, cols uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.console == 0 {
		return errors.New("terminal closed")
	}
	return windows.ResizePseudoConsole(p.console, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (p *conPTY) Wait() error {
	p.mu.Lock()
	h := p.process
	p.mu.Unlock()
	if h == 0 {
		return errors.New("process already reaped")
	}
	if _, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	err := windows.GetExitCodeProcess(h, &code)
	// Close under the lock so Kill never terminates a reused handle.
	p.mu.Lock()
	windows.CloseHandle(h)
	p.process = 0
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}

func (p *conPTY) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.process == 0 {
		return nil
	}
	return windows.TerminateProcess(p.process, 1)
}

// Close shuts the console down. That ends conhost, which breaks the output
// pipe and so unblocks a pending Read; the pipe handles go after it.
func (p *conPTY) Close() error {
	p.mu.Lock()
	console := p.console
	p.console = 0
	p.mu.Unlock()
	if console == 0 {
		return nil
	}
	windows.ClosePseudoConsole(console)
	p.in.Close()
	p.out.Close()
	return nil
}
