// Package config loads the service configuration from environment variables.
package config

import (
	"net"
	"os"
)

// Environment variable names and documented defaults. They are exported so the
// server's -h/--help text (cmd/api) can name the env equivalent and default of
// every flag without duplicating the values.
const (
	// EnvAddr is the environment variable holding the HTTP listen address.
	EnvAddr = "KANBAN_ADDR"
	// EnvDatabaseURL is the environment variable holding the Postgres DSN.
	EnvDatabaseURL = "KANBAN_DATABASE_URL"
	// EnvCommit overrides the build identifier reported by /api/healthz for
	// builds the Go toolchain did not VCS-stamp (see httpapi.buildCommit).
	EnvCommit = "KANBAN_COMMIT"
	// EnvTestDatabaseURL is the environment variable holding the DSN of the
	// dedicated test database. It is read by the test tooling, not by the
	// server binary itself.
	EnvTestDatabaseURL = "KANBAN_TEST_DATABASE_URL"

	// DefaultAddr binds loopback only (v1 has no auth).
	DefaultAddr = "127.0.0.1:18080"
	// DefaultDatabaseURL is the live dev/coordination-board database.
	DefaultDatabaseURL = "postgres://kanban:kanban@127.0.0.1:54329/kanban?sslmode=disable"
	// DefaultTestDatabaseURL is the isolated test database on the same instance.
	DefaultTestDatabaseURL = "postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable"
)

// Config holds the runtime configuration of the API server.
type Config struct {
	// Addr is the HTTP listen address (always bind loopback).
	Addr string
	// DatabaseURL is the Postgres connection string used by the server and
	// backs the live dev/coordination-board database.
	DatabaseURL string
	// TestDatabaseURL is the Postgres connection string used by tests. It points
	// at a dedicated kanban_test database so tests never write to the live board.
	//
	// DECISION (PB-105 review nit 1): Load reads it so the server and the test
	// tooling share one loader, but the running server never consults this
	// field. The -h/--help text and README say "read but unused by the server"
	// rather than claiming the binary does not read it.
	TestDatabaseURL string
}

// Load reads the configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Addr:            getenv(EnvAddr, DefaultAddr),
		DatabaseURL:     getenv(EnvDatabaseURL, DefaultDatabaseURL),
		TestDatabaseURL: getenv(EnvTestDatabaseURL, DefaultTestDatabaseURL),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// IsLoopbackAddr reports whether addr binds the loopback interface only
// (127.0.0.0/8, ::1, or localhost). v1 has no authentication, so the server
// refuses to listen on any other host.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
