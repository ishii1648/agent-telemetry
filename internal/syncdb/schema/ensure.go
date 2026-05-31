package schema

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrEventsTableMissing reports that the events table — the source of truth the
// derived VIEWs are defined over — does not exist, so the VIEWs cannot be
// (re)created. The DB was never built by sync-db. Callers surface this as a
// hint to run `agent-telemetry sync-db` first, instead of letting a downstream
// query fail with a cryptic "no such table: pr_metrics" (issue 0052).
var ErrEventsTableMissing = errors.New("events table not found — run `agent-telemetry sync-db` first")

// PRMetricsViewPresent reports whether the pr_metrics aggregate VIEW exists. It
// is the canonical probe for "are the derived relations intact": every
// dashboard panel and the flush metrics path read it, and it sits at the top of
// the VIEW dependency chain (joining sessions + transcript_stats), so its
// absence means the derived layer needs repair.
func PRMetricsViewPresent(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'view' AND name = 'pr_metrics'`,
	).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// EnsureViews repairs the derived VIEWs/triggers when the pr_metrics VIEW is
// missing, by applying ViewsSQL. It is non-destructive: ViewsSQL only drops and
// recreates VIEWs and triggers, never the events table, so it is safe to call
// on a populated DB — unlike the full schema.SQL, which drops events.
//
// This is the self-heal the schema_hash skip in sync-db / server EnsureSchema
// cannot do on its own (a matching hash skips all DDL, so a VIEW dropped
// out-of-band — or a DB built by a binary predating the VIEW — stays broken).
// flush's metrics path requires the VIEW, so it calls this before reading it
// (issue 0052).
//
// When the events table itself is absent the VIEWs cannot be defined over it;
// EnsureViews returns ErrEventsTableMissing so the caller can print an
// actionable hint rather than a cryptic downstream error.
func EnsureViews(db *sql.DB) error {
	present, err := PRMetricsViewPresent(db)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`,
	).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrEventsTableMissing
	}
	if _, err := db.Exec(ViewsSQL); err != nil {
		return fmt.Errorf("apply views: %w", err)
	}
	return nil
}
