package main

import (
	"flag"
	"fmt"
	"io"

	"api-first-kanban/internal/config"
)

// usageText is the -h/--help output for the single server binary. Every flag
// is listed with its environment-variable equivalent and default value; the
// values come from internal/config so this text cannot drift from the loader.
// It is a plain string (not flag.PrintDefaults) so the env equivalents can be
// shown next to each default.
func usageText() string {
	return fmt.Sprintf(`kanban-server — API-first kanban: REST + MCP + web UI on one loopback port.

Usage:
  kanban-server [flags]

Flags:
  -addr string
        HTTP listen address. Must be a loopback host: the server refuses to
        bind a non-loopback address (v1 has no authentication).
        env: %s (default %q)

  -database-url string
        Postgres connection string (DSN) used by the server; goose migrations
        run automatically at startup.
        env: %s (default %q)

  -h, --help
        Print this help and exit 0.

Environment:
  %s
        HTTP listen address (equivalent to -addr).
  %s
        Postgres DSN (equivalent to -database-url).
  %s
        Build identifier reported by /api/healthz; overrides the Go
        toolchain's VCS stamping for builds without it.

Precedence: flags override environment variables, which override the defaults
shown above.

Test-only variables (used by go test / the e2e tooling; the server never
consults them):
  %s
        DSN of the isolated test database. config.Load reads it into Config,
        but the running server does not use that field.
  KANBAN_TEST_STRICT
        Fail instead of skip when the test database is down.

See README.md ("CLI flags") for the full contract.
`, config.EnvAddr, config.DefaultAddr, config.EnvDatabaseURL, config.DefaultDatabaseURL,
		config.EnvAddr, config.EnvDatabaseURL, config.EnvCommit, config.EnvTestDatabaseURL)
}

// parseFlags parses the server's command-line flags over the environment
// configuration. The effective precedence is flag > env > default: only flags
// that were explicitly passed override the env-derived config, so a left-empty
// flag keeps the env value.
//
// help reports that -h/--help was requested; the usage text has already been
// written to stdout and the caller should exit 0. Parse errors are reported by
// the flag package (message + usage on stderr) and also returned.
func parseFlags(args []string, env config.Config, stdout, stderr io.Writer) (cfg config.Config, help bool, err error) {
	fs := flag.NewFlagSet("kanban-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText()) }

	// Defaults mirror the documented ones; env values are merged in below via
	// fs.Visit, so they only win when the flag was NOT passed.
	addr := fs.String("addr", config.DefaultAddr, "HTTP listen address (loopback host only)")
	databaseURL := fs.String("database-url", config.DefaultDatabaseURL, "Postgres connection string (DSN)")
	helpLong := fs.Bool("help", false, "print this help and exit")
	helpShort := fs.Bool("h", false, "print this help (shorthand)")

	// Both -h and --help are defined explicitly, so the flag package's own
	// ErrHelp path never fires; the help flags are ordinary booleans we handle
	// ourselves (help on stdout, exit 0), and real parse errors go to stderr.
	if err := fs.Parse(args); err != nil {
		return config.Config{}, false, err
	}
	if *helpLong || *helpShort {
		fmt.Fprint(stdout, usageText())
		return env, true, nil
	}

	cfg = env
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "addr":
			cfg.Addr = *addr
		case "database-url":
			cfg.DatabaseURL = *databaseURL
		}
	})
	return cfg, false, nil
}
