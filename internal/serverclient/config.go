// Package serverclient implements `agent-telemetry flush`: extracting unsent
// events from the local sync-db SQLite and POSTing them as OTLP/HTTP Logs to a
// central server's /v1/logs endpoint.
//
// The on-disk contract (state.json `last_flushed_sequence`, OTLP payload shape)
// is documented in docs/spec.md ## サーバ送信. The reasons behind the
// append-only event-sourced design are recorded in
// issues/closed/0038-spec-event-sourced-metrics-otel.md (it supersedes the
// /v1/metrics aggregated-row push from 0009).
package serverclient

import (
	"bufio"
	"os"
	"strings"

	"github.com/ishii1648/agent-telemetry/internal/configpath"
)

// ServerConfig holds the resolved [server] section from agent-telemetry.toml.
// Both fields must be non-empty for flush to attempt a network call; that gate
// is enforced by Configured().
type ServerConfig struct {
	Endpoint string
	Token    string
}

// Configured reports whether the [server] section is populated enough to send
// a request. A missing config is intentionally not an error — `agent-telemetry
// flush` is meant to be safe in cron without first checking whether the user
// has opted into server upload.
func (c ServerConfig) Configured() bool {
	return c.Endpoint != "" && c.Token != ""
}

// ConfigPath returns the resolved path of agent-telemetry's TOML config
// (XDG path with ~/.claude fallback for legacy installs). Delegates to
// configpath.Resolve so the migration warning fires once per process even
// when both serverclient and userid touch the file.
func ConfigPath() string {
	return configpath.Resolve()
}

// LoadConfig reads the [server] section of the TOML file. Missing file or
// missing keys returns a zero-value ServerConfig with no error — the caller
// inspects Configured() to decide whether to proceed.
//
// The parser is intentionally minimal (no nested tables, no arrays) to match
// userid.readConfigUser. Adding a real TOML library is overkill for the two
// keys this project reads.
func LoadConfig(path string) (ServerConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ServerConfig{}, nil
		}
		return ServerConfig{}, err
	}
	defer f.Close()

	cfg := ServerConfig{}
	inServer := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inServer = strings.HasPrefix(line, "[server]")
			continue
		}
		if !inServer {
			continue
		}
		key, value, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "endpoint":
			cfg.Endpoint = unquote(value)
		case "token":
			cfg.Token = unquote(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return ServerConfig{}, err
	}
	return cfg, nil
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
