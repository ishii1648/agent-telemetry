// Package serverclient implements `agent-telemetry flush`: extracting unsent
// events from the local sync-db SQLite and POSTing them as OTLP/HTTP to one or
// more configured export targets (a central agent-telemetry-server, an OTel
// Collector, or a backend OTLP intake such as Datadog).
//
// The on-disk contract (config `[[export]]` targets, state.json per-target
// cursors, OTLP payload shape) is documented in docs/spec.md ## サーバ送信. The
// reasons behind the append-only event-sourced design are recorded in
// issues/closed/0038-spec-event-sourced-metrics-otel.md; the pluggable export
// target design is recorded in
// issues/closed/0040-design-pluggable-otlp-export-backends.md and implemented
// per issues/0042-feat-flush-export-target-array.md.
package serverclient

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/ishii1648/agent-telemetry/internal/configpath"
)

// Default values applied to an ExportTarget when the corresponding config key
// is omitted. The defaults reproduce the legacy single-[server] behavior
// (Bearer auth, JSON OTLP, logs only) so existing configs keep working.
const (
	defaultAuthHeader = "Authorization"
	defaultAuthScheme = "Bearer"

	// Allowed `encoding` values (issue 0048 allow-list). signalJSON is also the
	// default applied when the key is omitted.
	signalJSON      = "json"
	signalProtobuf  = "protobuf"
	defaultEncoding = signalJSON

	// Allowed `signals` values (issue 0048 allow-list).
	signalLogs    = "logs"
	signalMetrics = "metrics"

	// legacyServerTargetID is the stable target_id synthesized for a config
	// that only has the legacy [server] section. Keeping it stable lets the
	// per-target cursor (state.json flush_cursors) survive the migration from
	// the old single last_flushed_sequence field.
	legacyServerTargetID = "server"
)

// ExportTarget is one resolved OTLP/HTTP destination. It carries everything
// that differs between a central server, an OTel Collector, and a backend
// intake: where to send, how to authenticate, and how to encode the wire bytes.
//
// ID is the *stable* identity used as the per-target cursor key in state.json.
// It is intentionally decoupled from Endpoint so that changing a URL does not
// orphan the cursor and re-send every event (0040 cursor contract).
type ExportTarget struct {
	ID         string
	Endpoint   string
	Token      string
	AuthHeader string   // HTTP header carrying credentials (e.g. "Authorization", "dd-api-key")
	AuthScheme string   // value prefix (e.g. "Bearer"); empty means the raw token
	Encoding   string   // "json" or "protobuf"
	Signals    []string // representations to send: "logs" (this issue), "metrics" (0043)
}

// Configured reports whether the target can attempt a network call. An empty
// endpoint or token is treated as "not opted in" rather than an error, so
// flush stays safe in cron.
func (t ExportTarget) Configured() bool {
	return t.Endpoint != "" && t.Token != ""
}

// SendsLogs reports whether this target wants the raw-events (OTLP Logs)
// representation. Targets default to logs-only; the pr_metrics gauge (OTLP
// Metrics) representation is opt-in via signals = ["metrics"].
func (t ExportTarget) SendsLogs() bool {
	return t.hasSignal(signalLogs)
}

// SendsMetrics reports whether this target wants the pre-aggregated pr_metrics
// gauge (OTLP Metrics, last-value) representation (issue 0043). A target may
// send both ("logs" and "metrics"); each representation rides its own cursor.
func (t ExportTarget) SendsMetrics() bool {
	return t.hasSignal(signalMetrics)
}

func (t ExportTarget) hasSignal(want string) bool {
	for _, s := range t.Signals {
		if s == want {
			return true
		}
	}
	return false
}

// ConfigPath returns the resolved path of agent-telemetry's TOML config
// (XDG path with ~/.claude fallback for legacy installs). Delegates to
// configpath.Resolve so the migration warning fires once per process even
// when both serverclient and userid touch the file.
func ConfigPath() string {
	return configpath.Resolve()
}

// LoadConfig reads the export targets from the TOML file. It accepts two
// shapes, which may coexist:
//
//   - The legacy [server] section (endpoint + token), normalized into a single
//     target with the stable id "server", Bearer auth, JSON encoding, logs only.
//   - One or more [[export]] array-of-tables entries, each a full ExportTarget
//     with per-target auth header/scheme, encoding, and signals.
//
// Missing file or no targets returns an empty slice with no error — the caller
// decides whether to proceed based on whether any target is Configured().
//
// The parser is intentionally minimal (line-based, only the keys this project
// reads) to match userid.readConfigUser; a full TOML library is overkill for
// the handful of keys here. Array-of-tables support is limited to the flat
// [[export]] shape documented in docs/spec.md.
func LoadConfig(path string) ([]ExportTarget, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var server ExportTarget // legacy [server] accumulator
	hasServer := false
	var exports []ExportTarget
	section := "" // "", "server", or "export" (current [[export]] table)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[[export]]") {
			section = "export"
			exports = append(exports, ExportTarget{})
			continue
		}
		if strings.HasPrefix(line, "[") {
			if strings.HasPrefix(line, "[server]") {
				section = "server"
				hasServer = true
			} else {
				section = ""
			}
			continue
		}
		key, value, ok := splitKV(line)
		if !ok {
			continue
		}
		switch section {
		case "server":
			switch key {
			case "endpoint":
				server.Endpoint = unquote(value)
			case "token":
				server.Token = expandEnv(unquote(value))
			}
		case "export":
			applyExportKey(&exports[len(exports)-1], key, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var targets []ExportTarget
	if hasServer {
		server.ID = legacyServerTargetID
		normalizeTarget(&server)
		targets = append(targets, server)
	}
	for i := range exports {
		if exports[i].ID == "" {
			return nil, fmt.Errorf("[[export]] entry #%d missing required `id`", i+1)
		}
		normalizeTarget(&exports[i])
		targets = append(targets, exports[i])
	}
	if err := assertUniqueIDs(targets); err != nil {
		return nil, err
	}
	for i := range targets {
		if err := validateTarget(targets[i]); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

// applyExportKey sets one key on the in-progress [[export]] target.
func applyExportKey(t *ExportTarget, key, value string) {
	switch key {
	case "id":
		t.ID = unquote(value)
	case "endpoint":
		t.Endpoint = unquote(value)
	case "token":
		t.Token = expandEnv(unquote(value))
	case "auth_header":
		t.AuthHeader = unquote(value)
	case "auth_scheme":
		t.AuthScheme = unquote(value)
	case "encoding":
		t.Encoding = unquote(value)
	case "signals":
		t.Signals = parseStringArray(value)
	}
}

// normalizeTarget fills in defaults for any unset field so callers never see
// an empty auth header / encoding / signal list.
func normalizeTarget(t *ExportTarget) {
	if t.AuthHeader == "" {
		t.AuthHeader = defaultAuthHeader
	}
	// AuthScheme is intentionally left as-is: an explicit empty string is a
	// valid choice (raw token, no prefix — e.g. dd-api-key). We only default
	// it when the key was never present, which the parser can't distinguish
	// from "" on its own; the [server] legacy path and the documented default
	// both want "Bearer", so we default only when the header is the default.
	if t.AuthHeader == defaultAuthHeader && t.AuthScheme == "" {
		t.AuthScheme = defaultAuthScheme
	}
	if t.Encoding == "" {
		t.Encoding = defaultEncoding
	}
	if len(t.Signals) == 0 {
		t.Signals = []string{signalLogs}
	}
}

// validateTarget rejects non-empty but unknown `encoding` / `signals` values
// (issue 0048). normalizeTarget runs first, so empty values are already
// defaulted (json / ["logs"]); this only ever sees configured values. Unknown
// values are fail-fast like a missing/duplicate id rather than silently
// fed to encodeBatch's JSON default branch (encoding) or dropped from both
// logs/metrics target sets (signals), which made flush mislabel the wire or
// report "export target 設定なし" with exit 0.
func validateTarget(t ExportTarget) error {
	switch t.Encoding {
	case signalJSON, signalProtobuf:
	default:
		return fmt.Errorf("export target %q: unknown encoding %q (allowed: %q, %q)", t.ID, t.Encoding, signalJSON, signalProtobuf)
	}
	for _, s := range t.Signals {
		switch s {
		case signalLogs, signalMetrics:
		default:
			return fmt.Errorf("export target %q: unknown signal %q (allowed: %q, %q)", t.ID, s, signalLogs, signalMetrics)
		}
	}
	return nil
}

// assertUniqueIDs rejects duplicate target ids: cursors are keyed by id, so two
// targets sharing an id would clobber each other's cursor.
func assertUniqueIDs(targets []ExportTarget) error {
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t.ID] {
			return fmt.Errorf("duplicate export target id %q (ids must be unique; they key the per-target cursor)", t.ID)
		}
		seen[t.ID] = true
	}
	return nil
}

// parseStringArray parses a minimal inline TOML string array:
// `["logs", "metrics"]`. Anything it can't parse yields an empty slice, which
// normalizeTarget then defaults to ["logs"].
func parseStringArray(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		s := unquote(strings.TrimSpace(part))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// expandEnv resolves a ${VAR} reference so secrets (Datadog API keys) can live
// in the environment instead of being written into config.toml. Only the exact
// form "${VAR}" is expanded; any other value is returned verbatim so tokens
// that legitimately contain "$" are not mangled.
func expandEnv(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		name := v[2 : len(v)-1]
		if name != "" {
			return os.Getenv(name)
		}
	}
	return v
}

func splitKV(line string) (key, value string, ok bool) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eq])
	value = strings.TrimSpace(line[eq+1:])
	if key == "" {
		return "", "", false
	}
	value = stripInlineComment(value)
	return key, value, true
}

func stripInlineComment(value string) string {
	if strings.HasPrefix(value, `"`) {
		if end := strings.IndexByte(value[1:], '"'); end >= 0 {
			head := value[:end+2]
			tail := value[end+2:]
			if hash := strings.IndexByte(tail, '#'); hash >= 0 {
				tail = tail[:hash]
			}
			return strings.TrimSpace(head + tail)
		}
		return value
	}
	if hash := strings.IndexByte(value, '#'); hash >= 0 {
		return strings.TrimSpace(value[:hash])
	}
	return value
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}
