@echo off
title Discord Free Cloud
cls

echo Discord Free Cloud by zyrexdz
echo.

cd /d "%~dp0"

if exist "discord-free-cloud.exe" (
    echo Starting Discord Free Cloud...
    start "" "discord-free-cloud.exe"
    exit /b
)

if exist "discord-storage-engine.exe" (
    echo Starting Discord Free Cloud...
    start "" "discord-storage-engine.exe"
    exit /b
)

where go >nul 2>nul
if %errorlevel% equ 0 (
    echo Building app...
    go build -buildvcs=false -ldflags="-s -w" -o discord-free-cloud.exe ./cmd/app
    if %errorlevel% neq 0 (
        echo [ERROR] Build failed. Make sure Go is installed.
        pause
        exit /b
    )
    echo Starting Discord Free Cloud...
    start "" "discord-free-cloud.exe"
    exit /b
)

echo [ERROR] Could not find discord-free-cloud.exe or Go compiler.
echo Please run build.bat first or download the release.
echo.
pause
