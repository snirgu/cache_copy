@echo off
echo Building for Windows...
set GOOS=windows
set GOARCH=amd64
go build -mod=vendor -o bin\cache_copy.exe main.go

echo Building for Linux...
set GOOS=linux
set GOARCH=amd64
go build -mod=vendor -o bin\cache_copy main.go

echo Build completed!