@echo off
rem Windows entry point for the Makefile targets: make.cmd <target>
rem Bypasses the PowerShell execution policy so it works out of the box.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0make.ps1" %*
exit /b %ERRORLEVEL%
