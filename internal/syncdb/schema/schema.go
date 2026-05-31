// Package schema embeds the SQLite DDL shared by client (sync-db) and
// server (agent-telemetry-server). Keeping it dep-free means the server
// binary does not pull in transcript parsing or sessionindex code.
package schema

import _ "embed"

//go:generate go run ./genhash

// tablesSQL is the durable part of the schema (the events table + schema_meta
// + indexes). Applying it DROPs and recreates the events table, so it is only
// safe on a fresh/rebuilt DB — never as a repair on a populated one.
//
//go:embed schema.sql
var tablesSQL string

// ViewsSQL is the derived part: all VIEWs and the INSTEAD OF INSERT triggers.
// It is idempotent and non-destructive (it never touches the events table), so
// EnsureViews can reapply it to heal a DB whose aggregate VIEWs went missing
// (issue 0052) without losing stored events.
//
//go:embed views.sql
var ViewsSQL string

// SQL is the full schema: durable tables followed by the derived relations.
// genhash computes Hash over this same composition (tablesSQL + "\n" + ViewsSQL),
// so editing either file changes the hash and triggers a rebuild.
var SQL = tablesSQL + "\n" + ViewsSQL
