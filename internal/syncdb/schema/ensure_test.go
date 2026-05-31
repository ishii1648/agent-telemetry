package schema

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// freshDB opens a temp DB and applies the full schema (tables + views), so it
// starts with every derived relation present.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(SQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

// TestExpectedDerivedRelations pins the set parsed out of ViewsSQL so a future
// edit to views.sql that the regex fails to pick up is caught here rather than
// silently shrinking the self-heal coverage.
func TestExpectedDerivedRelations(t *testing.T) {
	got := map[string]bool{}
	for _, n := range expectedDerivedRelations() {
		got[n] = true
	}
	want := []string{
		"sessions", "transcript_stats",
		"sessions_insert_events", "transcript_stats_insert_events",
		"session_intervals", "session_concurrency_daily", "session_concurrency_weekly",
		"pr_metrics", "pr_merged_at_approx", "weekly_pr_metrics", "weekly_session_metrics",
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("expectedDerivedRelations missing %q", n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("expectedDerivedRelations count: got %d, want %d (%v)", len(got), len(want), expectedDerivedRelations())
	}
}

// TestEnsureViews_HealsMissingTrigger covers the relation EnsureViews must repair
// even though pr_metrics is intact: a dropped INSTEAD OF INSERT trigger. Probing
// pr_metrics alone would miss it, and the next sync-db's INSERT INTO sessions
// would then fail against a trigger-less VIEW.
func TestEnsureViews_HealsMissingTrigger(t *testing.T) {
	db := freshDB(t)
	if _, err := db.Exec("DROP TRIGGER sessions_insert_events"); err != nil {
		t.Fatal(err)
	}
	present, err := DerivedRelationsPresent(db)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("DerivedRelationsPresent must report false when a trigger is missing")
	}
	if err := EnsureViews(db); err != nil {
		t.Fatalf("EnsureViews: %v", err)
	}
	// The trigger is back: an INSERT INTO the sessions VIEW now succeeds.
	if _, err := db.Exec(`INSERT INTO sessions
		(session_id, coding_agent, agent_version, user_id, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, backfill_checked, is_merged, review_comments, changes_requested)
		VALUES ('s1','claude','v1','alice','2026-05-10T10:00:00Z','','u/r','feat/x','','',' ','','','',0,0,0,0)`); err != nil {
		t.Errorf("INSERT INTO sessions after heal: %v (trigger not recreated?)", err)
	}
}

// TestEnsureViews_HealsMissingWeeklyView covers a derived VIEW that the flush
// metrics path does not read but dashboards do: dropping it must still trigger
// a repair even with pr_metrics present.
func TestEnsureViews_HealsMissingWeeklyView(t *testing.T) {
	db := freshDB(t)
	if _, err := db.Exec("DROP VIEW weekly_session_metrics"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureViews(db); err != nil {
		t.Fatalf("EnsureViews: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='view' AND name='weekly_session_metrics'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("weekly_session_metrics not recreated")
	}
}

// TestEnsureViews_NoopWhenIntact verifies the hot path is a no-op: with every
// relation present EnsureViews returns nil without reapplying DDL.
func TestEnsureViews_NoopWhenIntact(t *testing.T) {
	db := freshDB(t)
	present, err := DerivedRelationsPresent(db)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("a freshly-built DB must have all derived relations present")
	}
	if err := EnsureViews(db); err != nil {
		t.Fatalf("EnsureViews on intact DB: %v", err)
	}
}

// TestEnsureViews_EventsTableMissing verifies the actionable error for a DB that
// was never built by sync-db (no events table to define the VIEWs over).
func TestEnsureViews_EventsTableMissing(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureViews(db); !errors.Is(err, ErrEventsTableMissing) {
		t.Fatalf("EnsureViews on empty DB: got %v, want ErrEventsTableMissing", err)
	}
}
