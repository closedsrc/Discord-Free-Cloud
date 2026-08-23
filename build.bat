@echo off
title Build Discord Free Cloud
cls

echo ==================================================================
echo              BUILDING DISCORD FREE CLOUD
echo ==================================================================
echo.

cd /d "%~dp0"

where go >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] Go compiler not found!
    echo Please install Go from https://golang.org/dl/
    echo.
    pause
    exit /b 1
)

echo Compiling discord-free-cloud.exe...
go build -buildvcs=false -ldflags="-s -w" -o discord-free-cloud.exe ./cmd/app

if %errorlevel% equ 0 (
    echo.
    echo [SUCCESS] Built discord-free-cloud.exe successfully!
    echo.
) else (
    echo.
    echo [ERROR] Build failed!
    echo.
)

pause
