# local.vibe setup (Windows) — checks Go, builds, installs the binary, and
# wires up DNS / port forwarding when Phase 2 of Windows support lands.
#
# Run from an elevated PowerShell:
#     powershell.exe -ExecutionPolicy Bypass -File .\setup.ps1
#
# This is the Windows analog of setup.sh. It mirrors the same flow:
#   1. Ensure Go is installed (winget install GoLang.Go if missing)
#   2. go build -o vibe.exe .
#   3. Copy vibe.exe to %LOCALAPPDATA%\Programs\vibe\
#   4. Hand off to `vibe setup` for the platform-specific system config
#      (currently a stub on Windows — Phase 2 of the cross-platform port
#      will wire up the embedded DNS stub, netsh portproxy, certutil cert
#      trust, and a Scheduled Task on logon).
#   5. Start the daemon.

$ErrorActionPreference = 'Stop'

function Step($msg)  { Write-Host "`n$msg" -ForegroundColor White }
function OK($msg)    { Write-Host "  $msg" -ForegroundColor Green }
function Info($msg)  { Write-Host "  $msg" -ForegroundColor DarkGray }
function Fail($msg)  { Write-Host "  $msg" -ForegroundColor Red; exit 1 }

Write-Host "local.vibe — friendly names for local dev servers" -ForegroundColor White

# --- Go --------------------------------------------------------------------
Step "Checking Go..."
$go = Get-Command go.exe -ErrorAction SilentlyContinue
if ($go) {
    OK ("Go {0} installed" -f ((& go version) -replace '^go version go([\d.]+).*$', '$1'))
} else {
    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if (-not $winget) {
        Fail "winget not found. Install Go manually from https://go.dev/dl/ then re-run this script."
    }
    Info "Installing Go via winget..."
    winget install --id GoLang.Go --accept-source-agreements --accept-package-agreements --silent
    # winget puts go on PATH but PATH refresh requires a new shell — pull
    # the canonical install location into this session so the build below works.
    $goBin = Join-Path $env:ProgramFiles 'Go\bin'
    if (Test-Path (Join-Path $goBin 'go.exe')) {
        $env:Path = "$goBin;$env:Path"
    }
    if (-not (Get-Command go.exe -ErrorAction SilentlyContinue)) {
        Fail "Go install via winget did not put go.exe on PATH. Open a new PowerShell and re-run this script."
    }
    OK "Go installed"
}

# --- Build -----------------------------------------------------------------
Step "Building vibe..."
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $scriptDir
& go build -o vibe.exe .
if ($LASTEXITCODE -ne 0) { Fail "go build failed" }
OK "Built successfully"

# --- Install binary --------------------------------------------------------
Step "Installing binary..."
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\vibe'
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}
$installPath = Join-Path $installDir 'vibe.exe'
Copy-Item -Path (Join-Path $scriptDir 'vibe.exe') -Destination $installPath -Force
OK "Installed to $installPath"

# Add the install dir to the user's PATH if it's not already there. Done at
# the User scope so it persists across sessions; the current shell still
# needs $env:Path updated for the daemon-start step below to find vibe.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    OK "Added $installDir to user PATH (open a new shell to pick this up globally)"
}
$env:Path = "$installDir;$env:Path"

# --- System setup ----------------------------------------------------------
Step "Configuring system (DNS, port forwarding, autostart)..."
Info "Note: Windows system setup is currently a stub — Phase 2 of the cross-platform port will wire this up."
Info "For now this prints what manual steps to run."
& $installPath setup
# Don't fail the script if setup is a stub; it returns non-zero on purpose.

# --- Start daemon ----------------------------------------------------------
Step "Starting daemon..."
& $installPath daemon start
OK "Daemon started"

Write-Host ""
Write-Host "Setup complete!" -ForegroundColor Green
Write-Host ""
Write-Host "  Dashboard:  http://localhost:7999  (until DNS is wired up)"
Write-Host "  Once Phase 2 lands: https://local.vibe"
Write-Host ""
