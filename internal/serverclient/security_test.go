package serverclient

import (
	"strings"
	"testing"
)

// TestIsInsecurePlaintext pins the loopback-allow / non-loopback-http-flag
// policy for issue 0059: only cleartext http:// to a non-loopback host is
// insecure; https, loopback http, and malformed/empty endpoints are not.
func TestIsInsecurePlaintext(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"https remote", "https://telemetry.example.com/v1/logs", false},
		{"http remote host", "http://telemetry.example.com/v1/logs", true},
		{"http remote ip", "http://10.0.0.5:4318", true},
		{"http localhost", "http://localhost:4318", false},
		{"http 127.0.0.1", "http://127.0.0.1:4318", false},
		{"http 127.x loopback", "http://127.5.6.7:4318", false},
		{"http ipv6 loopback", "http://[::1]:4318", false},
		{"https localhost", "https://localhost:4318", false},
		{"uppercase scheme", "HTTP://telemetry.example.com", true},
		{"empty endpoint", "", false},
		{"grpc scheme", "grpc://telemetry.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExportTarget{Endpoint: tc.endpoint}.IsInsecurePlaintext()
			if got != tc.want {
				t.Errorf("IsInsecurePlaintext(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}

// TestInsecurePlaintextTargets verifies only Configured targets are scanned and
// that the credential flag escalates when a token rides the plaintext request.
func TestInsecurePlaintextTargets(t *testing.T) {
	targets := []ExportTarget{
		{ID: "secure", Endpoint: "https://remote.example.com", Signals: []string{signalLogs}},
		{ID: "local", Endpoint: "http://localhost:4318", Signals: []string{signalLogs}},
		{ID: "leaky", Endpoint: "http://remote.example.com", Token: "secret", Signals: []string{signalLogs}},
		{ID: "plain", Endpoint: "http://remote.example.com", Signals: []string{signalLogs}},
		{ID: "no-endpoint", Endpoint: "", Signals: []string{signalLogs}}, // not Configured
		{ID: "no-signal", Endpoint: "http://remote.example.com"},         // not Configured
	}
	got := InsecurePlaintextTargets(targets)
	if len(got) != 2 {
		t.Fatalf("want 2 insecure targets, got %d: %+v", len(got), got)
	}
	byID := map[string]InsecureTarget{}
	for _, g := range got {
		byID[g.ID] = g
	}
	if _, ok := byID["leaky"]; !ok {
		t.Fatalf("missing leaky target: %+v", got)
	}
	if !byID["leaky"].HasCredential {
		t.Errorf("leaky target should be flagged HasCredential")
	}
	if byID["plain"].HasCredential {
		t.Errorf("plain target should not be flagged HasCredential")
	}
}

// TestSummarizeWarnsInsecure pins the operator-facing warning text on flush.
func TestSummarizeWarnsInsecure(t *testing.T) {
	r := &FlushResult{
		PerAgent: map[string]*FlushAgentResult{},
		InsecureTargets: []InsecureTarget{
			{ID: "leaky", Endpoint: "http://remote.example.com", HasCredential: true},
			{ID: "plain", Endpoint: "http://remote.example.com", HasCredential: false},
		},
	}
	var sb strings.Builder
	r.Summarize(&sb)
	out := sb.String()
	if !strings.Contains(out, "leaky") || !strings.Contains(out, "token が平文で漏洩") {
		t.Errorf("credential warning missing:\n%s", out)
	}
	if !strings.Contains(out, "plain") || !strings.Contains(out, "https:// の利用を推奨") {
		t.Errorf("plaintext warning missing:\n%s", out)
	}
}
