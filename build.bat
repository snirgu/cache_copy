# win
$env:GOARCH = "amd64"
$env:GOOS = "windows"
go build -mod=vendor -o ./bin/cache_copy.exe main.go

# linux
$env:GOARCH = "amd64"
$env:GOOS = "linux"
go build -mod=vendor -o ./bin/cache_copy main.go