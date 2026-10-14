// Version of the kanban server, surfaced once at startup.
package main

// Version is the release build identifier. Unstamped builds (plain
// `go build` / `go run` / `go test`) report "dev"; `make release` overrides it
// with `-ldflags "-X main.Version=$(git describe --tags --always)"`, so a
// running release binary can be identified without a separate --version flag.
var Version = "dev"
