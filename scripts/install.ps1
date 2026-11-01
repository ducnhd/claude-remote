# Windows counterpart of `make install`: build, copy the binary and static\
# to ~\bin, then register the logon task and (re)start it.
#
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1
#
# The server serves the static\ folder next to its binary, so skipping the
# copy means UI changes never reach the phone.
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $HOME 'bin'
$exe = Join-Path $bin 'claude-remote.exe'
$built = Join-Path $root 'claude-remote.exe'

# A shell opened before Go was installed does not have it on PATH yet.
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) {
    $go = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
    if (-not (Test-Path $go)) { throw 'Go not found: winget install GoLang.Go, then open a new terminal' }
}

Push-Location $root
try {
    & $go build -o $built .
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally {
    Pop-Location
}

New-Item -ItemType Directory -Force $bin | Out-Null

# Windows will not overwrite a running exe. Ending the task only stops its
# conhost, so stop the server itself too, then retry the copy while it exits.
cmd /c "schtasks /End /TN claude-remote >nul 2>&1"
Get-Process claude-remote -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $exe } |
    Stop-Process -Force -ErrorAction SilentlyContinue
for ($i = 0; ; $i++) {
    try {
        Copy-Item $built $exe -Force
        break
    } catch {
        if ($i -ge 10) { throw "cannot replace $exe (still running?): $_" }
        Start-Sleep -Seconds 1
    }
}

$static = Join-Path $bin 'static'
New-Item -ItemType Directory -Force $static | Out-Null
Copy-Item (Join-Path $root 'static\*') $static -Recurse -Force

# The skill lets "handoff to phone" work in every project, not just this one.
$skill = Join-Path $HOME '.claude\skills\claude-remote'
New-Item -ItemType Directory -Force $skill | Out-Null
Copy-Item (Join-Path $root '.claude\skills\claude-remote\SKILL.md') $skill -Force

& $exe install
if ($LASTEXITCODE -ne 0) { throw 'claude-remote install failed' }

Write-Host ''
Write-Host "Installed $exe"
Write-Host "Add $bin to your PATH to run claude-remote from any shell."
Write-Host 'First time: claude-remote setup, then claude-remote qr'
