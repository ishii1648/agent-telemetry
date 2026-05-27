package serverclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/backfill"
	"github.com/ishii1648/agent-telemetry/internal/syncdb"
)

// MaxBatchBytes is the per-request hard cap from docs/spec.md. Kept as a var
// so tests can shrink it without producing massive fixtures.
var MaxBatchBytes = 50 * 1024 * 1024

// gzipThreshold is the payload size above which we compress. Below this,
// gzip overhead (~30 bytes minimum) is comparable to the savings on small
// JSON, so we send raw.
const gzipThreshold = 4 * 1024

// requestTimeout caps each HTTP attempt. Server work is a dumb INSERT OR
// IGNORE into SQLite, so anything beyond this points at network trouble.
const requestTimeout = 60 * time.Second

type FlushOptions struct {
	ClientVersion string
	SinceLast     bool
	Full          bool
	DryRun        bool
	AgentName     string
	DBPath        string
	ConfigPath    string
	HTTPClient    *http.Client
}

type FlushResult struct {
	PerAgent map[string]*FlushAgentResult
}

type FlushAgentResult struct {
	Eligible      int
	Sent          int
	Skipped       int
	Batches       int
	PayloadBytes  int64
	Rejected      int
	RejectedError string
	NoConfig      bool
	StateUpdated  bool
	DryRun        bool
}

type EventRow struct {
	LocalSequence int64
	EventID       string
	OccurredAt    string
	SessionID     string
	CodingAgent   string
	EventName     string
	Attributes    json.RawMessage
}

type otlpValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
	BoolValue   bool   `json:"boolValue,omitempty"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpPayload struct {
	ResourceLogs []resourceLog `json:"resourceLogs"`
}

type resourceLog struct {
	Resource  otlpResource `json:"resource"`
	ScopeLogs []scopeLog   `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type scopeLog struct {
	Scope      otlpScope   `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type logRecord struct {
	TimeUnixNano         string          `json:"timeUnixNano"`
	ObservedTimeUnixNano string          `json:"observedTimeUnixNano,omitempty"`
	SeverityNumber       int             `json:"severityNumber"`
	EventName            string          `json:"eventName"`
	Attributes           []otlpAttribute `json:"attributes"`
}

type partialSuccess struct {
	RejectedLogRecords int    `json:"rejectedLogRecords"`
	ErrorMessage       string `json:"errorMessage"`
}

type flushResponse struct {
	PartialSuccess partialSuccess `json:"partialSuccess"`
}

func RunFlush(ctx context.Context, opts FlushOptions) (*FlushResult, error) {
	if opts.DBPath == "" {
		opts.DBPath = syncdb.DBPath()
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: requestTimeout}
	}
	if !opts.Full && !opts.SinceLast {
		opts.SinceLast = true
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	agents, err := agent.ResolveOrDetect(opts.AgentName)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}
	db, err := sql.Open("sqlite", opts.DBPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	res := &FlushResult{PerAgent: make(map[string]*FlushAgentResult, len(agents))}
	var firstErr error
	for _, a := range agents {
		ar, err := runFlushForAgent(ctx, db, a, cfg, opts)
		res.PerAgent[a.Name] = ar
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return res, firstErr
}

func runFlushForAgent(ctx context.Context, db *sql.DB, a *agent.Agent, cfg ServerConfig, opts FlushOptions) (*FlushAgentResult, error) {
	ar := &FlushAgentResult{DryRun: opts.DryRun}
	state, err := backfill.LoadState(a.StatePath())
	if err != nil {
		return ar, fmt.Errorf("load state[%s]: %w", a.Name, err)
	}
	after := state.LastFlushedSequence
	if opts.Full {
		after = 0
	}
	events, err := LoadEvents(db, a.Name, after)
	if err != nil {
		return ar, fmt.Errorf("load events[%s]: %w", a.Name, err)
	}
	ar.Sent = len(events)
	ar.Eligible = ar.Sent
	if !opts.Full && state.LastFlushedSequence > 0 {
		var total int
		_ = db.QueryRow(`SELECT COUNT(*) FROM events WHERE coding_agent = ?`, a.Name).Scan(&total)
		ar.Eligible = total
		ar.Skipped = total - ar.Sent
	}

	batches, err := splitEventBatches(events, opts.ClientVersion, MaxBatchBytes)
	if err != nil {
		return ar, err
	}
	ar.Batches = len(batches)
	for _, b := range batches {
		body, _ := json.Marshal(b)
		ar.PayloadBytes += int64(len(body))
	}

	if !cfg.Configured() {
		ar.NoConfig = true
		return ar, nil
	}
	if opts.DryRun {
		return ar, nil
	}
	endpoint, err := logsURL(cfg.Endpoint)
	if err != nil {
		return ar, err
	}
	for _, b := range batches {
		resp, sentBytes, err := postLogsBatch(ctx, opts.HTTPClient, endpoint, cfg.Token, b)
		ar.PayloadBytes += int64(sentBytes)
		// A transport failure (network error / non-2xx) means the batch never
		// landed: return without advancing the cursor so the next flush resends
		// the same range. postLogsBatch already maps >=400 to an error.
		if err != nil {
			return ar, err
		}
		// HTTP 2xx + rejectedLogRecords>0 is OTLP partial success: the server
		// permanently rejected those records (failed validation — missing
		// event_id/session_id/etc.) and logged them. Per the OTLP spec a partial
		// success MUST NOT be retried; resending the same malformed records would
		// loop forever. So we count them, surface a warning, and let the cursor
		// advance past the batch. See docs/spec.md ## プロトコル.
		ar.Rejected += resp.PartialSuccess.RejectedLogRecords
		if resp.PartialSuccess.RejectedLogRecords > 0 && resp.PartialSuccess.ErrorMessage != "" {
			ar.RejectedError = resp.PartialSuccess.ErrorMessage
		}
	}
	if len(events) > 0 {
		state.LastFlushedSequence = events[len(events)-1].LocalSequence
		if err := backfill.SaveState(a.StatePath(), state); err != nil {
			return ar, fmt.Errorf("save state[%s]: %w", a.Name, err)
		}
		ar.StateUpdated = true
	}
	return ar, nil
}

func LoadEvents(db *sql.DB, codingAgent string, after int64) ([]EventRow, error) {
	rows, err := db.Query(`
SELECT local_sequence, event_id, occurred_at, session_id, coding_agent, event_name, attributes
FROM events
WHERE coding_agent = ? AND local_sequence > ?
ORDER BY local_sequence`, codingAgent, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var attrs string
		if err := rows.Scan(&e.LocalSequence, &e.EventID, &e.OccurredAt, &e.SessionID, &e.CodingAgent, &e.EventName, &attrs); err != nil {
			return nil, err
		}
		if attrs == "" {
			attrs = "{}"
		}
		e.Attributes = json.RawMessage(attrs)
		out = append(out, e)
	}
	return out, rows.Err()
}

func logsURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/logs"
	return u.String(), nil
}

func splitEventBatches(events []EventRow, clientVersion string, maxBytes int) ([]otlpPayload, error) {
	if len(events) == 0 {
		return nil, nil
	}
	var batches []otlpPayload
	var cur []EventRow
	for _, e := range events {
		next := append(append([]EventRow{}, cur...), e)
		p, err := buildOTLPPayload(next, clientVersion)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(p)
		if len(cur) > 0 && len(body) > maxBytes {
			prev, err := buildOTLPPayload(cur, clientVersion)
			if err != nil {
				return nil, err
			}
			batches = append(batches, prev)
			cur = []EventRow{e}
			continue
		}
		cur = next
	}
	if len(cur) > 0 {
		p, err := buildOTLPPayload(cur, clientVersion)
		if err != nil {
			return nil, err
		}
		batches = append(batches, p)
	}
	return batches, nil
}

func buildOTLPPayload(events []EventRow, clientVersion string) (otlpPayload, error) {
	records := make([]logRecord, 0, len(events))
	for _, e := range events {
		attrs := []otlpAttribute{
			stringAttr("event_id", e.EventID),
			intAttr("local_sequence", e.LocalSequence),
			stringAttr("session_id", e.SessionID),
			stringAttr("coding_agent", e.CodingAgent),
		}
		var extra map[string]any
		if err := json.Unmarshal(e.Attributes, &extra); err != nil {
			return otlpPayload{}, fmt.Errorf("decode event attrs %s: %w", e.EventID, err)
		}
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			attrs = append(attrs, anyAttr(k, extra[k]))
		}
		records = append(records, logRecord{
			TimeUnixNano:         unixNanoString(e.OccurredAt),
			ObservedTimeUnixNano: unixNanoString(e.OccurredAt),
			SeverityNumber:       9,
			EventName:            e.EventName,
			Attributes:           attrs,
		})
	}
	return otlpPayload{ResourceLogs: []resourceLog{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			stringAttr("service.name", "agent-telemetry"),
			stringAttr("service.version", clientVersion),
		}},
		ScopeLogs: []scopeLog{{
			Scope:      otlpScope{Name: "agent-telemetry/client"},
			LogRecords: records,
		}},
	}}}, nil
}

func anyAttr(key string, v any) otlpAttribute {
	switch x := v.(type) {
	case bool:
		return boolAttr(key, x)
	case float64:
		return intAttr(key, int64(x))
	case string:
		return stringAttr(key, x)
	default:
		b, _ := json.Marshal(x)
		return stringAttr(key, string(b))
	}
}

func stringAttr(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{StringValue: value}}
}

func intAttr(key string, value int64) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{IntValue: fmt.Sprintf("%d", value)}}
}

func boolAttr(key string, value bool) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{BoolValue: value}}
}

func unixNanoString(ts string) string {
	if ts == "" {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return fmt.Sprintf("%d", t.UnixNano())
		}
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func postLogsBatch(ctx context.Context, client *http.Client, endpoint, token string, p otlpPayload) (flushResponse, int, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return flushResponse{}, 0, fmt.Errorf("marshal payload: %w", err)
	}
	var reqBody io.Reader = bytes.NewReader(body)
	contentEncoding := ""
	wireSize := len(body)
	if len(body) >= gzipThreshold {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(body); err != nil {
			return flushResponse{}, 0, fmt.Errorf("gzip write: %w", err)
		}
		if err := gz.Close(); err != nil {
			return flushResponse{}, 0, fmt.Errorf("gzip close: %w", err)
		}
		reqBody = &buf
		contentEncoding = "gzip"
		wireSize = buf.Len()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reqBody)
	if err != nil {
		return flushResponse{}, 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if contentEncoding != "" {
		req.Header.Set("Content-Encoding", contentEncoding)
	}
	resp, err := client.Do(req)
	if err != nil {
		return flushResponse{}, wireSize, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return flushResponse{}, wireSize, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return flushResponse{}, wireSize, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var r flushResponse
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &r); err != nil {
			return flushResponse{}, wireSize, fmt.Errorf("decode response: %w", err)
		}
	}
	return r, wireSize, nil
}

func (r *FlushResult) Summarize(w io.Writer) {
	names := make([]string, 0, len(r.PerAgent))
	for name := range r.PerAgent {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ar := r.PerAgent[name]
		switch {
		case ar.NoConfig:
			fmt.Fprintf(w, "flush[%s]: [server] 設定なし — eligible=%d sent=0 (skipped network)\n", name, ar.Eligible)
		case ar.DryRun:
			fmt.Fprintf(w, "flush[%s] dry-run: eligible=%d sent=%d skipped=%d batches=%d payload=%d bytes\n",
				name, ar.Eligible, ar.Sent, ar.Skipped, ar.Batches, ar.PayloadBytes)
		default:
			fmt.Fprintf(w, "flush[%s]: eligible=%d sent=%d skipped=%d batches=%d payload=%d bytes rejected=%d\n",
				name, ar.Eligible, ar.Sent, ar.Skipped, ar.Batches, ar.PayloadBytes, ar.Rejected)
			if ar.Rejected > 0 {
				// Rejected records are permanently dropped (cursor advanced past
				// them); warn so the operator can inspect the server's rejected.log.
				fmt.Fprintf(w, "  warning: %d records permanently rejected by server (not retried): %s\n",
					ar.Rejected, ar.RejectedError)
			}
		}
	}
}
