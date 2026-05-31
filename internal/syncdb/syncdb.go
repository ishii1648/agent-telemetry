package syncdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
	"github.com/ishii1648/agent-telemetry/internal/transcript"
	"github.com/ishii1648/agent-telemetry/internal/userid"
)

const dummyPRURL = "https://github.com/org/repo/pull/123"

// DBPath returns the default path to agent-telemetry.db.
//
// The DB lives under ~/.claude/ even for Codex sessions for backward
// compatibility (existing Grafana datasources point here). The DB itself
// is multi-agent — distinguish via the coding_agent column.
func DBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agent-telemetry.db")
}

// Run rebuilds the SQLite database from every detected agent's
// session-index. Use RunForAgents to scope the rebuild to a single agent.
func Run() error {
	return RunForAgents(agent.Detect(), DBPath())
}

// RunForAgents rebuilds the SQLite DB by reading the session-index of each
// supplied agent. When agents is empty, falls back to Claude alone so old
// callers don't see an empty DB after the rename.
func RunForAgents(agents []*agent.Agent, dbPath string) error {
	if len(agents) == 0 {
		agents = []*agent.Agent{agent.Claude()}
	}
	sources := make([]agentSource, 0, len(agents))
	for _, a := range agents {
		sources = append(sources, agentSource{
			Agent:     a,
			IndexPath: a.SessionIndexPath(),
		})
	}
	return runWithSources(sources, dbPath)
}

// RunWithPaths preserves the legacy (pre-Codex) entry point used by
// tests and tooling that hard-codes a single Claude session-index.
func RunWithPaths(indexPath, dbPath string) error {
	return runWithSources([]agentSource{{
		Agent:     agent.Claude(),
		IndexPath: indexPath,
	}}, dbPath)
}

type agentSource struct {
	Agent     *agent.Agent
	IndexPath string
}

// ensureSchema applies schema.sql only when the embedded hash differs from the
// hash recorded in schema_meta. A missing schema_meta table or row is treated
// as a mismatch so legacy DBs and freshly-created files both rebuild cleanly.
// On the hot path (hash matches) no DDL runs, so concurrent Grafana queries
// don't see tables disappear mid-flight.
func ensureSchema(db *sql.DB) error {
	var current string
	err := db.QueryRow("SELECT value FROM schema_meta WHERE key = 'schema_hash'").Scan(&current)
	if err == nil && current == schema.Hash {
		// Hash matches, so the events table is current — but a derived VIEW may
		// have been dropped out-of-band (issue 0052). Repair it non-destructively
		// rather than skipping all DDL; never reapply the full schema here, which
		// would DROP the events table.
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

func runWithSources(sources []agentSource, dbPath string) error {
	// _pragma=busy_timeout は新規コネクションごとに適用される。サブエージェント
	// 終了などで Stop hook が並行発火して sync-db プロセスが多重起動された際、
	// 後続プロセスがライタロック解放を待てるようにして SQLITE_BUSY 即時失敗を防ぐ。
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("pragma: %w", err)
	}

	if err := ensureSchema(db); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sessionStmt, err := tx.Prepare(`INSERT OR REPLACE INTO sessions
		(session_id, coding_agent, agent_version, user_id, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, is_subagent, backfill_checked, is_merged, task_type, review_comments, changes_requested)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer sessionStmt.Close()

	statsStmt, err := tx.Prepare(`INSERT OR REPLACE INTO transcript_stats
		(session_id, coding_agent, tool_use_total, mid_session_msgs, ask_user_question, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens, model, is_ghost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer statsStmt.Close()

	// Resolve once per sync — cheap (one git invocation worst case) and
	// avoids redundant work when many sessions need backfilling.
	resolvedUser, _ := userid.Resolve()

	totalSessions, totalStats := 0, 0
	for _, src := range sources {
		raws, sessions, err := sessionindex.ReadAll(src.IndexPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read %s sessions: %w", src.Agent.Name, err)
		}

		// Backfill missing user_id directly into the JSONL so the file remains
		// the source of truth. We rewrite atomically only when at least one
		// row needs updating, to keep no-op syncs side-effect free.
		dirty := false
		for i := range sessions {
			if sessions[i].SessionID == "" {
				continue
			}
			if sessions[i].UserID != "" {
				continue
			}
			sessions[i].UserID = resolvedUser
			updated, err := sessionindex.MarshalSession(sessions[i])
			if err != nil {
				return fmt.Errorf("marshal session %s: %w", sessions[i].SessionID, err)
			}
			raws[i] = updated
			dirty = true
		}
		if dirty {
			if _, err := sessionindex.WithLockedIndex(src.IndexPath, func(latest []json.RawMessage, latestSessions []sessionindex.Session) ([]json.RawMessage, bool, error) {
				changed := false
				for i := range latestSessions {
					if latestSessions[i].SessionID == "" || latestSessions[i].UserID != "" {
						continue
					}
					latestSessions[i].UserID = resolvedUser
					updated, err := sessionindex.MarshalSession(latestSessions[i])
					if err != nil {
						return nil, false, err
					}
					latest[i] = updated
					changed = true
				}
				return latest, changed, nil
			}); err != nil {
				return fmt.Errorf("write %s sessions: %w", src.Agent.Name, err)
			}
		}

		// Deduplicate by session_id within a single agent (last wins).
		sessionMap := make(map[string]sessionindex.Session)
		for _, s := range sessions {
			if s.SessionID == "" {
				continue
			}
			sessionMap[s.SessionID] = s
		}

		fmt.Printf("sync-db: agent=%s — %d セッションを処理中...\n", src.Agent.Name, len(sessionMap))

		for _, s := range sessionMap {
			codingAgent := s.AgentName()

			prURL := ""
			if len(s.PRURLs) > 0 {
				prURL = s.PRURLs[len(s.PRURLs)-1]
			}
			if prURL == dummyPRURL {
				prURL = ""
			}

			isSubagent := 0
			if s.ParentSessionID != "" {
				isSubagent = 1
			}
			backfillChecked := 0
			if s.BackfillChecked {
				backfillChecked = 1
			}
			isMerged := 0
			if s.IsMerged {
				isMerged = 1
			}
			taskType := ExtractTaskType(s.Branch)

			prTitle := s.PRTitle
			if prURL == "" {
				prTitle = ""
			}

			if _, err := sessionStmt.Exec(
				s.SessionID, codingAgent, s.AgentVersion, s.User(), s.Timestamp, s.CWD, s.Repo, s.Branch,
				prURL, prTitle, s.Transcript, s.ParentSessionID, s.EndedAt, s.EndReason,
				isSubagent, backfillChecked, isMerged, taskType, s.ReviewComments, s.ChangesRequested,
			); err != nil {
				return fmt.Errorf("insert session %s/%s: %w", codingAgent, s.SessionID, err)
			}

			ts := transcript.Parse(s.Transcript, codingAgent)
			isGhost := 0
			if ts.IsGhost {
				isGhost = 1
			}
			if _, err := statsStmt.Exec(
				s.SessionID, codingAgent, ts.ToolUseTotal, ts.MidSessionMsgs, ts.AskUserQuestion,
				ts.InputTokens, ts.OutputTokens, ts.CacheWriteTokens, ts.CacheReadTokens,
				ts.ReasoningTokens, ts.Model, isGhost,
			); err != nil {
				return fmt.Errorf("insert stats %s/%s: %w", codingAgent, s.SessionID, err)
			}
			totalStats++
		}
		totalSessions += len(sessionMap)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	fmt.Printf("sync-db: 完了 — sessions: %d, transcript_stats: %d\n", totalSessions, totalStats)

	var prMetricsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&prMetricsCount); err != nil {
		prMetricsCount = 0
	}
	fmt.Printf("sync-db: pr_metrics VIEW: %d PR\n", prMetricsCount)

	return nil
}

// ExtractTaskType extracts the task type from a branch name prefix.
// e.g. "feat/add-metrics" → "feat", "fix/bug-42" → "fix", "main" → ""
func ExtractTaskType(branch string) string {
	parts := strings.SplitN(branch, "/", 2)
	if len(parts) == 2 {
		switch parts[0] {
		case "feat", "fix", "docs", "chore":
			return parts[0]
		}
	}
	return ""
}

// ShortenPRURL extracts "owner/repo#number" from a full GitHub PR URL.
func ShortenPRURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) >= 7 && parts[2] == "github.com" && parts[5] == "pull" {
		return parts[3] + "/" + parts[4] + "#" + parts[6]
	}
	return url
}
