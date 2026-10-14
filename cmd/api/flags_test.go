package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"api-first-kanban/internal/config"
)

// TestHelpFlagsPrintFullSurface pins the -h/--help contract: every flag is
// listed with its env-var equivalent and default, both spellings produce the
// same text, and no configuration is consumed (the returned cfg is the env
// config, untouched).
func TestHelpFlagsPrintFullSurface(t *testing.T) {
	env := config.Config{
		Addr:            "127.0.0.1:19999",
		DatabaseURL:     "postgres://env/db",
		TestDatabaseURL: "postgres://env/test",
	}
	wants := []string{
		"-addr", config.EnvAddr, config.DefaultAddr,
		"-database-url", config.EnvDatabaseURL, config.DefaultDatabaseURL,
		"-h, --help",
		"KANBAN_COMMIT",
		config.EnvTestDatabaseURL, "KANBAN_TEST_STRICT",
	}
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cfg, help, err := parseFlags([]string{arg}, env, &stdout, &stderr)
			if err != nil {
				t.Fatalf("parseFlags(%q) error: %v", arg, err)
			}
			if !help {
				t.Fatalf("parseFlags(%q) help = false, want true", arg)
			}
			if cfg != env {
				t.Errorf("parseFlags(%q) changed config: got %+v, want %+v", arg, cfg, env)
			}
			if stderr.Len() != 0 {
				t.Errorf("parseFlags(%q) wrote to stderr: %q", arg, stderr.String())
			}
			out := stdout.String()
			for _, want := range wants {
				if !strings.Contains(out, want) {
					t.Errorf("help output missing %q\n---\n%s", want, out)
				}
			}
		})
	}
}

// TestHelpFlagProcessExitsZero re-execs the real binary entrypoint (via the
// TestMain helper) so the exit code is verified, not just the parse result.
func TestHelpFlagProcessExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], arg)
			cmd.Env = append(os.Environ(), shutdownHelperEnv+"=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			if err != nil {
				t.Fatalf("%s: exit error %v (stderr: %s)", arg, err, stderr.String())
			}
			if code := cmd.ProcessState.ExitCode(); code != 0 {
				t.Fatalf("%s: exit code = %d, want 0", arg, code)
			}
			if !strings.Contains(stdout.String(), "-database-url") {
				t.Errorf("%s: usage not on stdout:\n%s", arg, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("%s: stderr should be empty, got %q", arg, stderr.String())
			}
		})
	}
}

// TestParseFlagsPrecedence pins flag > env > default merging: only explicitly
// passed flags override the env-derived config.
func TestParseFlagsPrecedence(t *testing.T) {
	env := config.Config{
		Addr:        "127.0.0.1:1111",
		DatabaseURL: "postgres://env/db",
	}
	cases := []struct {
		name string
		args []string
		want config.Config
	}{
		{"no flags keeps env", nil, env},
		{"flag overrides env", []string{"-addr", "127.0.0.1:2222"}, config.Config{Addr: "127.0.0.1:2222", DatabaseURL: env.DatabaseURL}},
		{"dsn flag overrides env", []string{"-database-url", "postgres://flag/db"}, config.Config{Addr: env.Addr, DatabaseURL: "postgres://flag/db"}},
		{"both flags override env", []string{"--addr=127.0.0.1:3333", "--database-url=postgres://flag2/db"}, config.Config{Addr: "127.0.0.1:3333", DatabaseURL: "postgres://flag2/db"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cfg, help, err := parseFlags(tc.args, env, &stdout, &stderr)
			if err != nil || help {
				t.Fatalf("parseFlags(%v) = help=%v err=%v", tc.args, help, err)
			}
			if cfg.Addr != tc.want.Addr || cfg.DatabaseURL != tc.want.DatabaseURL {
				t.Errorf("parseFlags(%v) = %+v, want %+v", tc.args, cfg, tc.want)
			}
		})
	}
}

// TestParseFlagsUnknownFlagErrors checks a bad flag is a hard error written to
// stderr (main exits 2), while the documented usage still gets printed.
func TestParseFlagsUnknownFlagErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, help, err := parseFlags([]string{"-bogus"}, config.Load(), &stdout, &stderr)
	if err == nil {
		t.Fatal("parseFlags(-bogus) error = nil, want non-nil")
	}
	if help {
		t.Error("parseFlags(-bogus) help = true, want false")
	}
	if stdout.Len() != 0 {
		t.Errorf("parseFlags(-bogus) wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "-bogus") || !strings.Contains(stderr.String(), "-database-url") {
		t.Errorf("stderr missing error/usage:\n%s", stderr.String())
	}
}
