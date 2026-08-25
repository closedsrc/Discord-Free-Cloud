@echo off
setlocal
cd /d "%~dp0"

if exist "discord-free-cloud.exe" (
    echo Launching Discord Free Cloud...
    start "" "discord-free-cloud.exe"
    exit /b
)

where go >nul 2>nul
if %errorlevel% equ 0 (
    echo Building binary...
    go build -buildvcs=false -ldflags="-s -w" -o discord-free-cloud.exe ./cmd/app
    if %errorlevel% neq 0 (
        echo Build failed. Make sure Go is installed.
        pause
        exit /b
    )
    echo Launching Discord Free Cloud...
    start "" "discord-free-cloud.exe"
    exit /b
)

echo Could not find discord-free-cloud.exe or Go compiler.
echo Run build.bat or download a release binary.
pause
