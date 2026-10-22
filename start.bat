@echo off
setlocal
cd /d "%~dp0"

if exist agent-cleaner.exe (
  agent-cleaner.exe
) else (
  where go >nul 2>nul
  if errorlevel 1 (
    echo Go 1.26.0 or newer is required to run from source.
    echo Build a standalone executable on a machine with Go, or install Go and retry.
    pause
    exit /b 1
  )
  go run .
)
if errorlevel 1 pause
