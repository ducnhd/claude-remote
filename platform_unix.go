//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// fold maps a name to the form it is compared in. Unix names (environment
// variables, paths) are case-sensitive, so it is the identity here.
func fold(s string) string { return s }

// extraPathDirs are prepended to the child PATH: launchd's PATH lacks the
// user and Homebrew bin dirs claude and its tools live in.
func extraPathDirs(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
}

// pbcopyPath is swapped out in tests.
var pbcopyPath = "/usr/bin/pbcopy"

// setClipboard puts text on the Mac clipboard.
func setClipboard(text string) error {
	cmd := exec.Command(pbcopyPath)
	// pbcopy reads stdin in the locale's encoding; launchd gives us no LANG,
	// which would turn every non-ASCII character into garbage.
	cmd.Env = childEnv()
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pbcopy: %v %s", err, out)
	}
	return nil
}

// Swapped out in tests.
var (
	wakeCmd = []string{"/usr/bin/caffeinate", "-u", "-t", "2"}
	lockCmd = []string{"/usr/bin/pmset", "displaysleepnow"}
)

// killChildrenOnExit is not needed under launchd, which stops the whole
// process group with the job.
func killChildrenOnExit() {}

// localScreenState has nothing to add on a Mac: claude-remote-screencap
// reports the lock state.
func localScreenState() ([]byte, bool) { return nil, false }

const vncOffMessage = "Screen Sharing is off: enable it in System Settings → General → Sharing"

const (
	restartHint            = "make install && launchctl kickstart -k gui/$(id -u)/com.claude-remote"
	cloudflaredInstallHint = "brew install cloudflared"
)

func autostartCheck() check {
	if out, err := exec.Command("launchctl", "list", "com.claude-remote").Output(); err == nil && len(out) > 0 {
		return check{"Auto-start", statusOK, "launchd job loaded", ""}
	}
	return check{"Auto-start", statusWarn, "launchd job not loaded", "claude-remote install"}
}

func cmdInstall() {
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(plistDir, 0755)
	plistPath := filepath.Join(plistDir, "com.claude-remote.plist")

	binPath, err := os.Executable()
	if err != nil {
		binPath = "/usr/local/bin/claude-remote"
	}

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	f, err := os.Create(plistPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := tmpl.Execute(f, map[string]string{
		"BinPath": binPath,
		"Home":    home,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing plist: %v\n", err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing plist: %v\n", err)
		os.Exit(1)
	}

	// bootout+bootstrap reloads the plist from disk. `load` is a no-op when
	// the job is already loaded, which silently keeps the old config running.
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", domain+"/com.claude-remote").Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: launchctl bootstrap failed: %v %s\n", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("Installed and loaded: %s\n", plistPath)
}

func cmdUninstall() {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.claude-remote.plist")
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/com.claude-remote", os.Getuid())).Run()
	exec.Command("launchctl", "unload", plistPath).Run() // older macOS fallback
	os.Remove(plistPath)
	fmt.Println("Uninstalled.")
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.claude-remote</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.BinPath}}</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.Home}}/.claude-remote/server.log</string>
  <key>StandardErrorPath</key>
  <string>{{.Home}}/.claude-remote/server.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
</dict>
</plist>
`
