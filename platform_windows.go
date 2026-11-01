//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fold maps a name to the form it is compared in. Windows environment
// variables and NTFS paths are case-insensitive: "Path" is PATH and
// C:\Users\Me\Desktop is c:\users\me\desktop.
func fold(s string) string { return strings.ToUpper(s) }

// extraPathDirs are prepended to the child PATH: the native Claude installer
// puts claude.exe in ~/.local/bin and npm puts claude.cmd in %APPDATA%\npm,
// and a scheduled task may start with neither on its PATH.
func extraPathDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm"))
	}
	return dirs
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// setClipboard puts text on the Windows clipboard as UTF-16, so the screen
// page can paste non-ASCII text with Ctrl+V.
func setClipboard(text string) error {
	data, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	// The clipboard is opened per thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Another app may hold the clipboard for a moment.
	opened := false
	for i := 0; i < 10 && !opened; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !opened {
		return errors.New("clipboard is busy")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	size := uintptr(len(data)) * unsafe.Sizeof(data[0])
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock: %w", err)
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&data[0])), size)
	procGlobalUnlock.Call(h)
	// On success the clipboard owns the memory.
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %w", err)
	}
	return nil
}

// Swapped out in tests. F15 counts as user input, waking the display without
// typing anything; LockWorkStation shows the lock screen.
var (
	wakeCmd = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"(New-Object -ComObject WScript.Shell).SendKeys('{F15}')"}
	lockCmd = []string{"rundll32.exe", "user32.dll,LockWorkStation"}
)

// killChildrenOnExit puts the server in a job object that kills every
// process in it once the last handle to the job closes — when the server
// exits, however it exits. Children (cloudflared, claude and its conhost)
// join the job automatically. Without it, a server terminated outright left
// them running, holding the port and the tunnel.
func killChildrenOnExit() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Printf("job object: %v", err)
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		log.Printf("job object limits: %v", err)
		windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		log.Printf("job object assign: %v", err)
		windows.CloseHandle(job)
	}
	// The handle stays open for the life of the process on purpose.
}

// stopServer ends the scheduled task and any server still running from
// binPath. Ending the task only terminates its conhost; the server itself
// keeps running (and keeps the exe locked) unless it is stopped here.
func stopServer(binPath string) {
	exec.Command("schtasks", "/End", "/TN", taskName).Run()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == self || !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), filepath.Base(binPath)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, pe.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil &&
			fold(windows.UTF16ToString(buf[:n])) == fold(binPath) {
			windows.TerminateProcess(h, 1)
		}
		windows.CloseHandle(h)
	}
}

// localScreenState answers /api/screen/state without screencap, which is
// macOS only: the screen is locked (or at the sign-in screen) while
// LogonUI.exe runs. The screen page uses it to offer the unlock form.
func localScreenState() ([]byte, bool) {
	locked, err := processRunning("LogonUI.exe")
	if err != nil {
		log.Printf("screen state: %v", err)
		return nil, false
	}
	out, _ := json.Marshal(map[string]any{"locked": locked, "asleep": false})
	return out, true
}

func processRunning(name string) (bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), name) {
			return true, nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, err
}

const vncOffMessage = "No VNC server on 127.0.0.1:5900: install one (TightVNC, UltraVNC) and allow loopback connections"

const (
	taskName               = "claude-remote"
	restartHint            = "claude-remote install   (re-registers and restarts the scheduled task)"
	cloudflaredInstallHint = "winget install --id Cloudflare.cloudflared"
)

func autostartCheck() check {
	if err := exec.Command("schtasks", "/Query", "/TN", taskName).Run(); err == nil {
		return check{"Auto-start", statusOK, "scheduled task registered", ""}
	}
	return check{"Auto-start", statusWarn, "scheduled task not registered", "claude-remote install"}
}

// cmdInstall registers a Task Scheduler task that starts the server at logon,
// in the user's desktop session (the clipboard and lock screen live there).
// conhost --headless gives it a console with no window, the way launchd gives
// the Mac build no terminal.
func cmdInstall() {
	cfg := getConfig()
	binPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var buf bytes.Buffer
	tmpl := template.Must(template.New("task").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(taskTemplate))
	if err := tmpl.Execute(&buf, map[string]string{
		"User":    u.Username,
		"BinPath": binPath,
		"Log":     filepath.Join(cfg.DataDir, "server.log"),
		"Dir":     filepath.Dir(binPath),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing task: %v\n", err)
		os.Exit(1)
	}
	xmlPath := filepath.Join(os.TempDir(), "claude-remote-task.xml")
	// schtasks reliably reads only UTF-16 task files.
	if err := os.WriteFile(xmlPath, utf16LE(buf.String()), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(xmlPath)

	// Stop a running copy first, and wait for its port: the new one would
	// otherwise fail to bind and exit.
	stopServer(binPath)
	for i := 0; i < 10 && portInUse(cfg.Port); i++ {
		time.Sleep(time.Second)
	}
	if out, err := exec.Command("schtasks", "/Create", "/TN", taskName, "/XML", xmlPath, "/F").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: schtasks /Create failed: %v %s\n", err, strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	if out, err := exec.Command("schtasks", "/Run", "/TN", taskName).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: schtasks /Run failed: %v %s\n", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("Installed scheduled task %q (starts at logon) and started it.\n", taskName)
	fmt.Printf("Log: %s\n", filepath.Join(cfg.DataDir, "server.log"))
}

func cmdUninstall() {
	if binPath, err := os.Executable(); err == nil {
		stopServer(binPath)
	}
	if out, err := exec.Command("schtasks", "/Delete", "/TN", taskName, "/F").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: schtasks /Delete failed: %v %s\n", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("Uninstalled.")
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// utf16LE encodes s as UTF-16 little endian with a byte order mark.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// RestartOnFailure stands in for launchd's KeepAlive; ExecutionTimeLimit PT0S
// stops Task Scheduler from killing the server after its default 72 hours.
const taskTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Claude Remote: control Claude Code from your phone</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>{{xml .User}}</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>{{xml .User}}</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>conhost.exe</Command>
      <Arguments>--headless "{{xml .BinPath}}" serve --log "{{xml .Log}}"</Arguments>
      <WorkingDirectory>{{xml .Dir}}</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
