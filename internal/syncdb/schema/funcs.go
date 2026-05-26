package schema

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"

	sqlite "modernc.org/sqlite"
)

// at_event_id derives a deterministic event_id from a canonical content
// string (event_name + coding_agent + session_id + the attributes JSON).
//
// The INSTEAD OF INSERT triggers in schema.sql call this so that re-running
// sync-db over unchanged source data produces the *same* event_id, which the
// triggers' INSERT OR IGNORE then dedups. Without a deterministic id every
// sync-db invocation would append a fresh random-id row per session, growing
// the events table without bound (and re-flushing duplicates). When the
// derived content actually changes (e.g. is_merged flips), the hash changes,
// a new snapshot row is appended, and the views resolve latest-wins via
// MAX(local_sequence). See docs/design.md ## event_id の採番.
//
// We register it here, alongside the embedded DDL that depends on it, so both
// the client (sync-db) and server (legacy /v1/metrics → sessions view) binaries
// — which both import this package — have it available before any connection
// opens. Read-only consumers (Grafana, raw sqlite3) never fire the triggers,
// so they don't need the function.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("at_event_id", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var content string
		switch v := args[0].(type) {
		case string:
			content = v
		case []byte:
			content = string(v)
		case nil:
			content = ""
		default:
			content = ""
		}
		sum := sha256.Sum256([]byte(content))
		return hex.EncodeToString(sum[:]), nil
	})
}
