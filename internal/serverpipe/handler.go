package serverpipe

import (
	"bytes"
	"compress/gzip"
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
)

// MaxPayloadBytes caps a single /v1/logs request. An OTLP batch is
// tiny in practice (<1 MB / month for individuals); 50 MB is
// insurance against runaway clients, matching docs/spec.md.
const MaxPayloadBytes = 50 * 1024 * 1024

// Handler holds the deps a /v1/logs request needs. RejectedLog is
// opened lazily so a missing data dir at startup doesn't kill the
// server before its first write.
type Handler struct {
	DB           *sql.DB
	RejectedPath string

	mu        sync.Mutex
	rejectedW io.WriteCloser
}

// NewHandler wires up the http.Handler. The ingest endpoint performs no
// authentication of its own — the trust boundary is network reachability
// (default loopback bind) plus a TLS-terminating proxy for public
// deployments. See docs/design.md "認証境界" and issue 0057.
func NewHandler(db *sql.DB, dataDir string) *Handler {
	return &Handler{
		DB:           db,
		RejectedPath: filepath.Join(dataDir, "rejected.log"),
	}
}

// Routes registers the ingest endpoint on the given mux. Kept
// separate from NewHandler so callers can compose middleware (e.g.
// access logging) before mounting.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/logs", h.ServeLogs)
}

// Close releases the rejected log file if it was opened.
func (h *Handler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var err error
	if h.rejectedW != nil {
		err = h.rejectedW.Close()
		h.rejectedW = nil
	}
	return err
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
	rejected, reasons, err := h.insertLogs(&payload)
	if len(reasons) > 0 {
		h.recordRejected(reasons)
	}
	resp := logsResponse{}
	resp.PartialSuccess.RejectedLogRecords = rejected
	if err != nil {
		resp.PartialSuccess.ErrorMessage = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if rejected > 0 {
		resp.PartialSuccess.ErrorMessage = fmt.Sprintf("%d log records rejected (logged to rejected.log)", rejected)
	}
	writeJSON(w, http.StatusOK, resp)
}

// insertLogs appends valid log records to events (INSERT OR IGNORE for
// idempotency). Records failing validation (missing required attrs) are
// permanently rejected: counted, described in reasons (for rejected.log), and
// skipped — never retried, matching OTLP partial-success semantics.
func (h *Handler) insertLogs(p *logsPayload) (int, []string, error) {
	tx, err := h.DB.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO events
		(event_id, occurred_at, received_at, session_id, coding_agent, event_name, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, nil, err
	}
	defer stmt.Close()

	rejected := 0
	var reasons []string
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rl := range p.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				eventID, sessionID, codingAgent, attrs := splitLogAttrs(lr.Attributes)
				if eventID == "" || sessionID == "" || codingAgent == "" || lr.EventName == "" {
					rejected++
					reasons = append(reasons, fmt.Sprintf("missing required attribute (event_id=%q session_id=%q coding_agent=%q event_name=%q)", eventID, sessionID, codingAgent, lr.EventName))
					continue
				}
				attrJSON, err := json.Marshal(attrs)
				if err != nil {
					rejected++
					reasons = append(reasons, fmt.Sprintf("event_id=%s: marshal attributes: %v", eventID, err))
					continue
				}
				if _, err := stmt.Exec(eventID, otlpTime(lr.TimeUnixNano), now, sessionID, codingAgent, lr.EventName, string(attrJSON)); err != nil {
					return rejected, reasons, fmt.Errorf("insert event %s: %w", eventID, err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return rejected, reasons, fmt.Errorf("commit: %w", err)
	}
	return rejected, reasons, nil
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

// recordRejected appends permanently-rejected OTLP log records to rejected.log
// so the data the client dropped (cursor advanced past it) leaves an audit
// trail an operator can inspect.
func (h *Handler) recordRejected(reasons []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.rejectedW == nil {
		if err := os.MkdirAll(filepath.Dir(h.RejectedPath), 0o755); err != nil {
			log.Printf("rejected.log mkdir: %v", err)
			return
		}
		f, err := os.OpenFile(h.RejectedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Printf("rejected.log open: %v", err)
			return
		}
		h.rejectedW = f
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range reasons {
		fmt.Fprintf(h.rejectedW, "%s %s\n", now, r)
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
