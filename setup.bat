@echo off
REM local.vibe setup wrapper for Windows.
REM
REM Why this exists: setup.ps1 can't print a friendly message when PowerShell's
REM execution policy blocks it, because the policy check happens before any
REM script code runs. This wrapper invokes powershell.exe with -ExecutionPolicy
REM Bypass on the command line, which side-steps the file-level policy check.
REM Cmd scripts aren't subject to PowerShell's policy, so users can run this
REM from any elevated cmd or PowerShell prompt without first running
REM Set-ExecutionPolicy.
REM
REM We invoke powershell.exe by absolute path under %SystemRoot% rather than
REM relying on PATH, because some user environments end up with a sanitized
REM PATH that doesn't include System32 -- and PowerShell-from-PATH then fails.
REM
REM Usage (from an elevated terminal):
REM   setup.bat            from elevated cmd
REM   .\setup.bat          from elevated PowerShell

setlocal

set SCRIPT_DIR=%~dp0

REM SystemRoot is set on every Windows install; falls back to C:\Windows.
set WINROOT=%SystemRoot%
if "%WINROOT%"=="" set WINROOT=C:\Windows

REM Prefer PowerShell 7 (pwsh) when present at its standard install location;
REM otherwise use Windows PowerShell 5.1, which is guaranteed to be at
REM %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe on every
REM Windows install.
set PWSH7="%ProgramFiles%\PowerShell\7\pwsh.exe"
set WINPS="%WINROOT%\System32\WindowsPowerShell\v1.0\powershell.exe"

if exist %PWSH7% (
    %PWSH7% -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%setup.ps1" %*
    exit /b %ERRORLEVEL%
)

if exist %WINPS% (
    %WINPS% -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%setup.ps1" %*
    exit /b %ERRORLEVEL%
)

echo ERROR: Could not find powershell.exe at any expected location:
echo   %PWSH7%
echo   %WINPS%
echo.
echo This is unusual -- one of those should always exist on Windows.
exit /b 1
