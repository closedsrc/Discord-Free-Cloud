@echo off
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if %errorlevel% neq 0 (
    echo Go compiler not found. Please install Go from https://golang.org/dl/
    pause
    exit /b 1
)

echo Building discord-free-cloud.exe...
go build -buildvcs=false -ldflags="-s -w" -o discord-free-cloud.exe ./cmd/app

if %errorlevel% equ 0 (
    echo Build complete.
) else (
    echo Build failed.
)

pause
