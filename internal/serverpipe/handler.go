package serverpipe

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// MaxPayloadBytes caps a single /v1/metrics request. Aggregated rows
// are tiny in practice (<1 MB / month for individuals); 50 MB is
// insurance against runaway clients, matching docs/spec.md.
const MaxPayloadBytes = 50 * 1024 * 1024

// Session mirrors the sessions table columns. JSON tags match
// docs/spec.md ## SQLite データモデル so payloads can be decoded
// directly without an intermediate shape.
type Session struct {
	SessionID        string `json:"session_id"`
	CodingAgent      string `json:"coding_agent"`
	AgentVersion     string `json:"agent_version"`
	UserID           string `json:"user_id"`
	Timestamp        string `json:"timestamp"`
	CWD              string `json:"cwd"`
	Repo             string `json:"repo"`
	Branch           string `json:"branch"`
	PRURL            string `json:"pr_url"`
	PRTitle          string `json:"pr_title"`
	Transcript       string `json:"transcript"`
	ParentSessionID  string `json:"parent_session_id"`
	EndedAt          string `json:"ended_at"`
	EndReason        string `json:"end_reason"`
	IsSubagent       int    `json:"is_subagent"`
	BackfillChecked  int    `json:"backfill_checked"`
	IsMerged         int    `json:"is_merged"`
	TaskType         string `json:"task_type"`
	ReviewComments   int    `json:"review_comments"`
	ChangesRequested int    `json:"changes_requested"`
}

// TranscriptStat mirrors the transcript_stats table columns.
type TranscriptStat struct {
	SessionID        string `json:"session_id"`
	CodingAgent      string `json:"coding_agent"`
	ToolUseTotal     int64  `json:"tool_use_total"`
	MidSessionMsgs   int64  `json:"mid_session_msgs"`
	AskUserQuestion  int64  `json:"ask_user_question"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	Model            string `json:"model"`
	IsGhost          int    `json:"is_ghost"`
}

// Payload is the request body for POST /v1/metrics.
type Payload struct {
	ClientVersion   string           `json:"client_version"`
	SchemaHash      string           `json:"schema_hash"`
	Sessions        []Session        `json:"sessions"`
	TranscriptStats []TranscriptStat `json:"transcript_stats"`
}

// Response is the body returned from POST /v1/metrics. Even when
// schema_mismatch is true the server returns 200 so clients can
// inspect the body — the client treats schema_mismatch as a hard
// stop and surfaces a binary-update prompt.
type Response struct {
	ReceivedSessions int  `json:"received_sessions"`
	SkippedSessions  int  `json:"skipped_sessions"`
	SchemaMismatch   bool `json:"schema_mismatch"`
}

// Handler holds the deps a /v1/metrics request needs. CollisionsLog
// is opened lazily so a missing data dir at startup doesn't kill the
// server before its first write.
type Handler struct {
	DB             *sql.DB
	Token          string
	CollisionsPath string

	mu          sync.Mutex
	collisionsW io.WriteCloser
}

// NewHandler wires up the http.Handler. Token must be non-empty —
// the cmd entrypoint validates the env var before reaching here.
func NewHandler(db *sql.DB, token, dataDir string) *Handler {
	return &Handler{
		DB:             db,
		Token:          token,
		CollisionsPath: filepath.Join(dataDir, "collisions.log"),
	}
}

// Routes registers the ingest endpoint on the given mux. Kept
// separate from NewHandler so callers can compose middleware (e.g.
// access logging) before mounting.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/metrics", h.ServeIngest)
	mux.HandleFunc("/v1/logs", h.ServeLogs)
}

// Close releases the collisions log file if it was opened.
func (h *Handler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.collisionsW != nil {
		err := h.collisionsW.Close()
		h.collisionsW = nil
		return err
	}
	return nil
}

func (h *Handler) ServeIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Schema mismatch is a hard stop. We respond 200 with the body so
	// the client can read schema_mismatch and surface the upgrade
	// prompt — using a non-2xx would conflate transport errors with
	// version drift.
	if payload.SchemaHash != schema.Hash {
		writeJSON(w, http.StatusOK, Response{
			SchemaMismatch: true,
		})
		return
	}

	resp, err := h.upsert(&payload)
	if err != nil {
		http.Error(w, "ingest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type logsPayload struct {
	ResourceLogs []struct {
		ScopeLogs []struct {
			LogRecords []struct {
				TimeUnixNano string `json:"timeUnixNano"`
				EventName    string `json:"eventName"`
				Attributes   []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
						IntValue    string `json:"intValue"`
						BoolValue   *bool  `json:"boolValue,omitempty"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type logsResponse struct {
	PartialSuccess struct {
		RejectedLogRecords int    `json:"rejectedLogRecords"`
		ErrorMessage       string `json:"errorMessage"`
	} `json:"partialSuccess"`
}

func (h *Handler) ServeLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var payload logsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	rejected, err := h.insertLogs(&payload)
	resp := logsResponse{}
	resp.PartialSuccess.RejectedLogRecords = rejected
	if err != nil {
		resp.PartialSuccess.ErrorMessage = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) insertLogs(p *logsPayload) (int, error) {
	tx, err := h.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO events
		(event_id, occurred_at, received_at, session_id, coding_agent, event_name, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	rejected := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rl := range p.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				eventID, sessionID, codingAgent, attrs := splitLogAttrs(lr.Attributes)
				if eventID == "" || sessionID == "" || codingAgent == "" || lr.EventName == "" {
					rejected++
					continue
				}
				attrJSON, err := json.Marshal(attrs)
				if err != nil {
					rejected++
					continue
				}
				if _, err := stmt.Exec(eventID, otlpTime(lr.TimeUnixNano), now, sessionID, codingAgent, lr.EventName, string(attrJSON)); err != nil {
					return rejected, fmt.Errorf("insert event %s: %w", eventID, err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return rejected, fmt.Errorf("commit: %w", err)
	}
	return rejected, nil
}

func splitLogAttrs(attrs []struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
		IntValue    string `json:"intValue"`
		BoolValue   *bool  `json:"boolValue,omitempty"`
	} `json:"value"`
}) (eventID, sessionID, codingAgent string, rest map[string]any) {
	rest = map[string]any{}
	for _, a := range attrs {
		var v any
		switch {
		case a.Value.IntValue != "":
			if n, err := strconv.ParseInt(a.Value.IntValue, 10, 64); err == nil {
				v = n
			} else {
				v = a.Value.IntValue
			}
		case a.Value.BoolValue != nil:
			v = *a.Value.BoolValue
		default:
			v = a.Value.StringValue
		}
		switch a.Key {
		case "event_id":
			eventID, _ = v.(string)
		case "session_id":
			sessionID, _ = v.(string)
		case "coding_agent":
			codingAgent, _ = v.(string)
		case "local_sequence":
			continue
		default:
			rest[a.Key] = v
		}
	}
	return eventID, sessionID, codingAgent, rest
}

func otlpTime(ns string) string {
	if ns == "" {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	var n int64
	if _, err := fmt.Sscanf(ns, "%d", &n); err != nil {
		return ns
	}
	return time.Unix(0, n).UTC().Format(time.RFC3339Nano)
}

func (h *Handler) checkAuth(r *http.Request) bool {
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		return false
	}
	got := authz[len(prefix):]
	// Constant-time compare so an attacker can't time the token.
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.Token)) == 1
}

// readBody reads the request body, transparently decompressing gzip
// when Content-Encoding indicates it. Both the compressed transport
// frame and the decoded payload are capped at MaxPayloadBytes to
// defend against zip-bomb-style inputs.
func readBody(r *http.Request) ([]byte, error) {
	limited := io.LimitReader(r.Body, MaxPayloadBytes+1)
	src := limited
	var gz *gzip.Reader
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		var err error
		gz, err = gzip.NewReader(limited)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		src = io.LimitReader(gz, MaxPayloadBytes+1)
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > MaxPayloadBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
	}
	return body, nil
}

// upsert applies sessions + transcript_stats rows in a single
// transaction. Pre-existing rows for the same composite PK are logged
// to collisions.log (last-write-wins semantics, per docs/design.md).
func (h *Handler) upsert(p *Payload) (Response, error) {
	tx, err := h.DB.Begin()
	if err != nil {
		return Response{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	collidedSessions, err := h.findCollidingSessions(tx, p.Sessions)
	if err != nil {
		return Response{}, err
	}

	sessionStmt, err := tx.Prepare(`INSERT OR REPLACE INTO sessions
		(session_id, coding_agent, agent_version, user_id, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, is_subagent, backfill_checked, is_merged, task_type, review_comments, changes_requested)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return Response{}, err
	}
	defer sessionStmt.Close()

	statsStmt, err := tx.Prepare(`INSERT OR REPLACE INTO transcript_stats
		(session_id, coding_agent, tool_use_total, mid_session_msgs, ask_user_question, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens, model, is_ghost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return Response{}, err
	}
	defer statsStmt.Close()

	for _, s := range p.Sessions {
		if _, err := sessionStmt.Exec(
			s.SessionID, s.CodingAgent, s.AgentVersion, s.UserID, s.Timestamp, s.CWD, s.Repo, s.Branch,
			s.PRURL, s.PRTitle, s.Transcript, s.ParentSessionID, s.EndedAt, s.EndReason,
			s.IsSubagent, s.BackfillChecked, s.IsMerged, s.TaskType, s.ReviewComments, s.ChangesRequested,
		); err != nil {
			return Response{}, fmt.Errorf("insert session %s/%s: %w", s.CodingAgent, s.SessionID, err)
		}
	}

	for _, t := range p.TranscriptStats {
		if _, err := statsStmt.Exec(
			t.SessionID, t.CodingAgent, t.ToolUseTotal, t.MidSessionMsgs, t.AskUserQuestion,
			t.InputTokens, t.OutputTokens, t.CacheWriteTokens, t.CacheReadTokens,
			t.ReasoningTokens, t.Model, t.IsGhost,
		); err != nil {
			return Response{}, fmt.Errorf("insert stats %s/%s: %w", t.CodingAgent, t.SessionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Response{}, fmt.Errorf("commit: %w", err)
	}

	if len(collidedSessions) > 0 {
		h.recordCollisions(collidedSessions)
	}
	return Response{
		ReceivedSessions: len(p.Sessions),
		SkippedSessions:  0,
	}, nil
}

// findCollidingSessions returns the (coding_agent, session_id) pairs
// in the payload that already exist in the DB. We log them but still
// upsert (last-write-wins).
func (h *Handler) findCollidingSessions(tx *sql.Tx, sessions []Session) ([][2]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	stmt, err := tx.Prepare(`SELECT 1 FROM sessions WHERE session_id = ? AND coding_agent = ?`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var collisions [][2]string
	for _, s := range sessions {
		var dummy int
		err := stmt.QueryRow(s.SessionID, s.CodingAgent).Scan(&dummy)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("collision check %s/%s: %w", s.CodingAgent, s.SessionID, err)
		}
		collisions = append(collisions, [2]string{s.CodingAgent, s.SessionID})
	}
	return collisions, nil
}

func (h *Handler) recordCollisions(pairs [][2]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.collisionsW == nil {
		if err := os.MkdirAll(filepath.Dir(h.CollisionsPath), 0o755); err != nil {
			log.Printf("collisions.log mkdir: %v", err)
			return
		}
		f, err := os.OpenFile(h.CollisionsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Printf("collisions.log open: %v", err)
			return
		}
		h.collisionsW = f
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range pairs {
		fmt.Fprintf(h.collisionsW, "%s coding_agent=%s session_id=%s\n", now, p[0], p[1])
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf := &bytes.Buffer{}
	_ = json.NewEncoder(buf).Encode(body)
	// Client may have hung up — nothing actionable on write failure
	// past the headers, so swallow the error explicitly.
	_, _ = w.Write(buf.Bytes())
}
