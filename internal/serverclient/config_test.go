package serverclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targetByID is a test helper that finds a parsed target by its stable id.
func targetByID(targets []ExportTarget, id string) (ExportTarget, bool) {
	for _, t := range targets {
		if t.ID == id {
			return t, true
		}
	}
	return ExportTarget{}, false
}

// TestLoadConfig_LegacyServerSection pins backward compatibility: a config with
// only the legacy [server] section is normalized into a single target with the
// stable id "server", Bearer auth, JSON encoding, and the logs signal.
func TestLoadConfig_LegacyServerSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `user = "alice@example.com"

[server]
endpoint = "https://telemetry.example.com"
token = "secret-token"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("want 1 target, got %d", len(targets))
	}
	srv, ok := targetByID(targets, legacyServerTargetID)
	if !ok {
		t.Fatalf("missing legacy server target: %+v", targets)
	}
	if srv.Endpoint != "https://telemetry.example.com" {
		t.Errorf("endpoint: got %q", srv.Endpoint)
	}
	if srv.Token != "secret-token" {
		t.Errorf("token: got %q", srv.Token)
	}
	if srv.AuthHeader != "Authorization" || srv.AuthScheme != "Bearer" {
		t.Errorf("auth: got %q / %q, want Authorization / Bearer", srv.AuthHeader, srv.AuthScheme)
	}
	if srv.Encoding != "json" {
		t.Errorf("encoding: got %q, want json", srv.Encoding)
	}
	if !srv.SendsLogs() {
		t.Errorf("legacy server target should send logs: %+v", srv.Signals)
	}
	if !srv.Configured() {
		t.Error("Configured() = false")
	}
}

func TestLoadConfig_NoServerSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`user = "alice@example.com"`), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Errorf("want 0 targets on missing section, got %d: %+v", len(targets), targets)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	targets, err := LoadConfig(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("missing file produced %d targets", len(targets))
	}
}

// TestLoadConfig_TokenlessServerConfigured pins issue 0051: a target with an
// endpoint but no token is Configured(). The token is required only by backends
// that authenticate; a credential-free local collector (the OSS observability
// recipe) must still be a flush destination rather than being silently dropped.
func TestLoadConfig_TokenlessServerConfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[server]
endpoint = "https://only-endpoint"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := targetByID(targets, legacyServerTargetID)
	if !ok {
		t.Fatalf("want a server target: %+v", targets)
	}
	if srv.Token != "" {
		t.Errorf("token should be empty, got %q", srv.Token)
	}
	if !srv.Configured() {
		t.Error("tokenless target with an endpoint should be Configured() (issue 0051)")
	}
}

// TestLoadConfig_TokenlessExportConfigured covers the OSS observability recipe
// shape (issue 0050/0051): an [[export]] pointing at a localhost Collector with
// no token and both signals is Configured() and keeps its empty token.
func TestLoadConfig_TokenlessExportConfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "oss-collector"
endpoint = "http://localhost:4318"
encoding = "json"
signals = ["logs", "metrics"]
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	tgt, ok := targetByID(targets, "oss-collector")
	if !ok {
		t.Fatalf("missing oss-collector target: %+v", targets)
	}
	if tgt.Token != "" {
		t.Errorf("token should be empty, got %q", tgt.Token)
	}
	if !tgt.Configured() {
		t.Error("tokenless local collector should be Configured() (issue 0051)")
	}
	if !tgt.SendsLogs() || !tgt.SendsMetrics() {
		t.Errorf("both signals should be set: %+v", tgt.Signals)
	}
}

// TestLoadConfig_NoEndpointNotConfigured pins the other half of issue 0051: a
// target that is genuinely unset (no endpoint) stays NOT Configured(), so an
// endpoint typo is not mistaken for a valid tokenless destination.
func TestLoadConfig_NoEndpointNotConfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "broken"
signals = ["logs"]
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	tgt, ok := targetByID(targets, "broken")
	if !ok {
		t.Fatalf("want the broken target surfaced: %+v", targets)
	}
	if tgt.Configured() {
		t.Error("a target with no endpoint must not be Configured()")
	}
}

// TestLoadConfig_ExportArray covers the new [[export]] array-of-tables: an
// explicit id, a custom auth header with no scheme (raw token), protobuf
// encoding, and ${VAR} token expansion from the environment.
func TestLoadConfig_ExportArray(t *testing.T) {
	t.Setenv("DD_API_KEY_TEST", "dd-secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "central"
endpoint = "https://telemetry.example.com"
token = "tok-central"

[[export]]
id = "datadog"
endpoint = "https://otlp.datadoghq.com"
token = "${DD_API_KEY_TEST}"
auth_header = "dd-api-key"
encoding = "protobuf"
signals = ["logs"]
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("want 2 targets, got %d: %+v", len(targets), targets)
	}

	central, ok := targetByID(targets, "central")
	if !ok {
		t.Fatal("missing central target")
	}
	if central.AuthHeader != "Authorization" || central.AuthScheme != "Bearer" || central.Encoding != "json" {
		t.Errorf("central defaults wrong: %+v", central)
	}

	dd, ok := targetByID(targets, "datadog")
	if !ok {
		t.Fatal("missing datadog target")
	}
	if dd.Token != "dd-secret" {
		t.Errorf("env expansion failed: token=%q", dd.Token)
	}
	if dd.AuthHeader != "dd-api-key" {
		t.Errorf("auth_header: got %q", dd.AuthHeader)
	}
	if dd.AuthScheme != "" {
		t.Errorf("custom auth header should default to empty scheme (raw token), got %q", dd.AuthScheme)
	}
	if dd.Encoding != "protobuf" {
		t.Errorf("encoding: got %q", dd.Encoding)
	}
}

// TestLoadConfig_ServerAndExportCoexist verifies the legacy [server] and new
// [[export]] entries are both surfaced as targets.
func TestLoadConfig_ServerAndExportCoexist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[server]
endpoint = "https://legacy.example.com"
token = "legacy-tok"

[[export]]
id = "datadog"
endpoint = "https://otlp.datadoghq.com"
token = "dd"
auth_header = "dd-api-key"
encoding = "protobuf"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("want server + datadog, got %d: %+v", len(targets), targets)
	}
	if _, ok := targetByID(targets, legacyServerTargetID); !ok {
		t.Error("legacy server target missing")
	}
	if _, ok := targetByID(targets, "datadog"); !ok {
		t.Error("datadog target missing")
	}
}

func TestLoadConfig_DuplicateIDRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "dup"
endpoint = "https://a"
token = "t1"

[[export]]
id = "dup"
endpoint = "https://b"
token = "t2"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("duplicate target id must be rejected")
	}
}

func TestLoadConfig_ExportMissingIDRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
endpoint = "https://a"
token = "t1"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("[[export]] without id must be rejected")
	}
}

// TestLoadConfig_InvalidEncodingRejected pins issue 0048(a): a non-empty but
// unknown `encoding` must fail-fast rather than silently falling through to JSON
// while the dry-run/summary mislabels the wire as the bogus value.
func TestLoadConfig_InvalidEncodingRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "badenc"
endpoint = "http://127.0.0.1:9"
token = "t1"
encoding = "xml"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("unknown encoding must be rejected")
	}
	if !strings.Contains(err.Error(), "badenc") || !strings.Contains(err.Error(), "xml") {
		t.Errorf("error should name the target id and the bad value: %v", err)
	}
}

// TestLoadConfig_InvalidSignalRejected pins issue 0048(b): a non-empty but
// unknown `signals` value must fail-fast rather than silently disabling the
// target (which then reports "export target 設定なし").
func TestLoadConfig_InvalidSignalRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "badsig"
endpoint = "http://127.0.0.1:9"
token = "t1"
signals = ["traces"]
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("unknown signal must be rejected")
	}
	if !strings.Contains(err.Error(), "badsig") || !strings.Contains(err.Error(), "traces") {
		t.Errorf("error should name the target id and the bad value: %v", err)
	}
}

// TestLoadConfig_PartiallyInvalidSignalsRejected ensures validation rejects a
// signals array where only one element is unknown (the valid ones must not mask
// the bad one).
func TestLoadConfig_PartiallyInvalidSignalsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[[export]]
id = "mixedsig"
endpoint = "http://127.0.0.1:9"
token = "t1"
signals = ["logs", "traces"]
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("partially invalid signals must be rejected")
	}
	if !strings.Contains(err.Error(), "mixedsig") || !strings.Contains(err.Error(), "traces") {
		t.Errorf("error should name the target id and the bad value: %v", err)
	}
}
