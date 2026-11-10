@echo off
rem Prefer "make build"; this script is a fallback for machines without make.
set /p VERSION=<VERSION
set LDFLAGS=-s -w -X main.VERSION=%VERSION%

echo Building %VERSION% for Windows...
set GOOS=windows
set GOARCH=amd64
go build -mod=vendor -trimpath -ldflags "%LDFLAGS%" -o bin\cache_copy.exe .

echo Building %VERSION% for Linux...
set GOOS=linux
set GOARCH=amd64
go build -mod=vendor -trimpath -ldflags "%LDFLAGS%" -o bin\cache_copy .

echo Build completed!
