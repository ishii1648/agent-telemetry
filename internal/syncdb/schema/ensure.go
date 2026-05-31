package schema

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// ErrEventsTableMissing reports that the events table — the source of truth the
// derived VIEWs are defined over — does not exist, so the VIEWs cannot be
// (re)created. The DB was never built by sync-db. Callers surface this as a
// hint to run `agent-telemetry sync-db` first, instead of letting a downstream
// query fail with a cryptic "no such table: pr_metrics" (issue 0052).
var ErrEventsTableMissing = errors.New("events table not found — run `agent-telemetry sync-db` first")

// derivedRelationRe extracts the VIEW/TRIGGER names that ViewsSQL declares, so
// the presence check stays in sync with the DDL automatically — there is no
// hand-maintained list to drift when a VIEW or trigger is added to views.sql.
var derivedRelationRe = regexp.MustCompile(`(?im)^\s*CREATE\s+(?:VIEW|TRIGGER)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// expectedDerivedRelations returns every VIEW/TRIGGER name ViewsSQL creates.
func expectedDerivedRelations() []string {
	matches := derivedRelationRe.FindAllStringSubmatch(ViewsSQL, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// DerivedRelationsPresent reports whether every VIEW and trigger ViewsSQL
// declares currently exists. Checking the whole set — not just pr_metrics —
// matters because a single missing relation breaks a different consumer: a
// dropped INSTEAD OF INSERT trigger makes sync-db's `INSERT INTO sessions`
// fail, and a dropped weekly_* / session_concurrency_* VIEW breaks dashboards,
// even while pr_metrics itself is intact. Any one missing means the derived
// layer needs repair.
func DerivedRelationsPresent(db *sql.DB) (bool, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('view', 'trigger')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, name := range expectedDerivedRelations() {
		if !have[name] {
			return false, nil
		}
	}
	return true, nil
}

// EnsureViews repairs the derived VIEWs/triggers when any of them is missing, by
// applying ViewsSQL. It is non-destructive: ViewsSQL only drops and recreates
// VIEWs and triggers, never the events table, so it is safe to call on a
// populated DB — unlike the full schema.SQL, which drops events.
//
// This is the self-heal the schema_hash skip in sync-db / server EnsureSchema
// cannot do on its own (a matching hash skips all DDL, so a relation dropped
// out-of-band — or a DB built by a binary predating it — stays broken). flush's
// metrics path requires the pr_metrics VIEW, so it calls this before reading it
// (issue 0052).
//
// When the events table itself is absent the VIEWs cannot be defined over it;
// EnsureViews returns ErrEventsTableMissing so the caller can print an
// actionable hint rather than a cryptic downstream error.
func EnsureViews(db *sql.DB) error {
	present, err := DerivedRelationsPresent(db)
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
