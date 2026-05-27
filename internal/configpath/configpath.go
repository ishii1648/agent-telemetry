// Package configpath resolves the on-disk location of agent-telemetry's
// shared TOML configuration file.
//
// The canonical location is XDG-compliant:
//
//	$XDG_CONFIG_HOME/agent-telemetry/config.toml   (when XDG_CONFIG_HOME is set)
//	$HOME/.config/agent-telemetry/config.toml      (otherwise)
//
// Older releases stored the same file at $HOME/.claude/agent-telemetry.toml.
// That path is still read as a fallback for users who haven't migrated their
// dotfiles yet, with a one-time stderr warning per process.
//
// We deliberately do NOT use os.UserConfigDir(): on macOS it returns
// ~/Library/Application Support which splits the user's config across
// platforms in a way that's surprising for a CLI tool and breaks parity
// with the linux/dotfiles workflow this project assumes.
//
// The fallback is scheduled for removal in a future release; see
// issues/closed/0032-spec-config-path-xdg-migration.md.
package configpath

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// warnWriter is the destination for the migration warning. Tests swap it
// out via SetWarnWriterForTest so they can assert on (and silence) the
// stderr message.
var warnWriter io.Writer = os.Stderr

// warnOnce dedups the migration warning within a single process. flush is
// invoked from cron / launchd / systemd timers — repeating the warning
// each time userid and serverclient both touch the config would double the
// log noise without adding signal.
var warnOnce sync.Once

// Preferred returns the new XDG path without checking existence or
// emitting any warning. Use this when you need to display the canonical
// location to the user (doctor, setup, error messages).
func Preferred() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "agent-telemetry", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "agent-telemetry", "config.toml")
}

// Legacy returns the pre-migration path ($HOME/.claude/agent-telemetry.toml).
// Used by doctor's migration diagnostic and by Resolve's fallback logic.
func Legacy() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agent-telemetry.toml")
}

// Resolve returns the path that should actually be read.
//
// Resolution:
//  1. If the preferred (XDG) path exists, return it.
//  2. Otherwise if the legacy path exists, return it AND emit a one-time
//     migration warning to stderr.
//  3. Otherwise return the preferred path. Callers treat ENOENT as
//     "no config" (cron-safe — flush must not fail when the user hasn't
//     opted into server upload).
//
// Resolve is safe to call multiple times in one process; only the first
// time the legacy path is selected does the warning fire.
func Resolve() string {
	preferred := Preferred()
	if exists(preferred) {
		return preferred
	}
	legacy := Legacy()
	if exists(legacy) {
		warnOnce.Do(func() {
			fmt.Fprintf(warnWriter,
				"agent-telemetry: reading config from legacy path %s — please migrate to %s (the legacy path will be removed in a future release)\n",
				legacy, preferred)
		})
		return legacy
	}
	return preferred
}

// MigrationStatus reports which path is currently in effect. doctor uses
// this to print a dedicated migration line without re-triggering the
// stderr warning.
type MigrationStatus struct {
	Preferred       string
	Legacy          string
	PreferredExists bool
	LegacyExists    bool
}

// Status returns the current migration state without side effects (no
// warning emitted, even if the legacy file is the only one present).
func Status() MigrationStatus {
	preferred := Preferred()
	legacy := Legacy()
	return MigrationStatus{
		Preferred:       preferred,
		Legacy:          legacy,
		PreferredExists: exists(preferred),
		LegacyExists:    exists(legacy),
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SetWarnWriterForTest replaces the warning destination and resets the
// once gate so a test can assert that the warning fires (or doesn't) in
// isolation. Returns a restore func that puts the writer back and clears
// the gate again so subsequent tests start from a known state.
//
// sync.Once contains a Mutex and cannot be copied by value; we always
// assign a fresh zero-value Once on entry and exit instead of stashing
// the previous one.
func SetWarnWriterForTest(w io.Writer) func() {
	prev := warnWriter
	warnWriter = w
	warnOnce = sync.Once{}
	return func() {
		warnWriter = prev
		warnOnce = sync.Once{}
	}
}
