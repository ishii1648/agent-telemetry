// Package serverpipe implements the dumb ingest layer for
// agent-telemetry-server: receive append-only events from clients as
// OTLP/HTTP Logs (POST /v1/logs) and append them (INSERT OR IGNORE)
// into a shared SQLite DB. sessions / transcript_stats / pr_metrics are
// derived VIEWs over events; the transcript parsing / PR rollup that
// feeds those events runs on the client side. The server only shares
// the schema DDL via internal/syncdb/schema.
package serverpipe

import (
	"database/sql"
	"fmt"

	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// OpenDB opens the server SQLite DB with the same WAL + busy_timeout
// settings the client uses, then ensures the schema matches the
// embedded DDL hash.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	if err := EnsureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// EnsureSchema applies schema.SQL when the DB's recorded hash differs
// from the embedded one. Mirrors the client's sync-db logic so a fresh
// server DB and a stale one both rebuild cleanly.
func EnsureSchema(db *sql.DB) error {
	var current string
	err := db.QueryRow("SELECT value FROM schema_meta WHERE key = 'schema_hash'").Scan(&current)
	if err == nil && current == schema.Hash {
		// Hash matches — the events table the server is the store of is current.
		// A derived VIEW could still have been dropped out-of-band; repair it
		// non-destructively (issue 0052). Never reapply the full schema on a
		// hash match: schema.SQL drops the events table, which would wipe
		// received events.
		return schema.EnsureViews(db)
	}
	if err := dropLegacyRelations(db); err != nil {
		return err
	}
	if _, err := db.Exec(schema.SQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('schema_hash', ?)", schema.Hash); err != nil {
		return fmt.Errorf("write schema hash: %w", err)
	}
	return nil
}

func dropLegacyRelations(db *sql.DB) error {
	rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE name IN ('sessions', 'transcript_stats')`)
	if err != nil {
		return fmt.Errorf("inspect legacy relations: %w", err)
	}
	defer rows.Close()
	type rel struct {
		typ  string
		name string
	}
	var rels []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.typ, &r.name); err != nil {
			return err
		}
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rels {
		switch r.typ {
		case "table":
			if _, err := db.Exec("DROP TABLE IF EXISTS " + r.name); err != nil {
				return fmt.Errorf("drop table %s: %w", r.name, err)
			}
		case "view":
			if _, err := db.Exec("DROP VIEW IF EXISTS " + r.name); err != nil {
				return fmt.Errorf("drop view %s: %w", r.name, err)
			}
		}
	}
	return nil
}
