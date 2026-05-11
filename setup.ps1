# local.vibe setup (Windows) -- checks Go, builds, installs the binary, and
# wires up DNS / port forwarding via 'vibe setup'.
#
# Run from an elevated PowerShell session:
#   .\setup.ps1
#
# Or:  pwsh -ExecutionPolicy Bypass -File .\setup.ps1
#
# This script is the Windows analog of setup.sh. It mirrors the same flow:
#   1. Ensure Go is installed (winget install GoLang.Go if missing)
#   2. go build -o vibe.exe .
#   3. Copy vibe.exe to %LOCALAPPDATA%\Programs\vibe\
#   4. Hand off to 'vibe setup' for the system config (DNS, netsh portproxy,
#      certutil CA trust, Scheduled Task on logon)
#   5. Start the daemon

$ErrorActionPreference = 'Stop'

function Step($msg)  { Write-Host "`n$msg" -ForegroundColor White }
function OK($msg)    { Write-Host "  $msg" -ForegroundColor Green }
function Info($msg)  { Write-Host "  $msg" -ForegroundColor DarkGray }
function Fail($msg)  { Write-Host "  $msg" -ForegroundColor Red; exit 1 }

Write-Host "local.vibe -- friendly names for local dev servers" -ForegroundColor White

# --- Pre-flight: are we elevated? -----------------------------------------
# vibe setup needs Administrator to touch DNS, the certutil cert store,
# netsh portproxy, and Task Scheduler. If we're not elevated, fail loudly
# now rather than 30 lines into the script with a cryptic certutil error.
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host ""
    Write-Host "This script requires Administrator privileges." -ForegroundColor Red
    Write-Host ""
    Write-Host "Re-launch an elevated terminal (right-click PowerShell -> 'Run as administrator')" -ForegroundColor Yellow
    Write-Host "or use sudo on Windows 11 24H2+." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Then run again:    .\setup.bat" -ForegroundColor Yellow
    Write-Host "(setup.bat side-steps PowerShell's execution policy automatically.)" -ForegroundColor DarkGray
    exit 1
}

# --- Pre-flight: heads-up about execution policy --------------------------
# If we got this far the policy let us run, but it might be restrictive
# enough that running setup.ps1 directly (without setup.bat) next time
# will fail. Surface that so the user isn't surprised on a re-run.
$policy = Get-ExecutionPolicy
if ($policy -in @('Restricted', 'AllSigned')) {
    Info "PowerShell execution policy is '$policy' (restrictive)."
    Info "If you re-run setup.ps1 directly, prefer .\setup.bat instead -- it sets the"
    Info "bypass automatically. Or set the policy for this session with:"
    Info "  Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force"
}

# --- Go --------------------------------------------------------------------
Step "Checking Go..."
$go = Get-Command go.exe -ErrorAction SilentlyContinue
if ($go) {
    $goVersion = (& go version) 2>&1
    OK "Go installed: $goVersion"
} else {
    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if (-not $winget) {
        Fail "winget not found. Install Go manually from https://go.dev/dl/ then re-run this script."
    }
    Info "Installing Go via winget..."
    winget install --id GoLang.Go --accept-source-agreements --accept-package-agreements --silent
    # winget puts go on PATH but PATH refresh requires a new shell, so pull
    # the canonical install location into this session for the build below.
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
# User scope so it persists across sessions; we also update $env:Path so the
# daemon-start step below works in the current shell.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    OK "Added $installDir to user PATH (open a new shell to pick this up globally)"
}
$env:Path = "$installDir;$env:Path"

# --- System setup ----------------------------------------------------------
Step "Configuring system (DNS, port forwarding, cert trust, autostart)..."
Info "Requires Administrator. Will fail with a clear message if you didn't elevate."
& $installPath setup
if ($LASTEXITCODE -ne 0) { Fail "vibe setup failed (exit $LASTEXITCODE)" }

# --- Start daemon ----------------------------------------------------------
Step "Starting daemon..."
& $installPath daemon start
if ($LASTEXITCODE -ne 0) { Fail "vibe daemon start failed (exit $LASTEXITCODE)" }
OK "Daemon started"

Write-Host ""
Write-Host "Setup complete!" -ForegroundColor Green
Write-Host ""
Write-Host "  Dashboard:  https://local.vibe"
Write-Host "  Register a route:   vibe register myapp 3000"
Write-Host "  Open a route:       vibe open myapp"
Write-Host "  Stop the daemon:    vibe daemon stop"
Write-Host "  Roll everything back: vibe uninstall  (elevated)"
Write-Host ""
