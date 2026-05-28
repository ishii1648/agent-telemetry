package upgrade

import (
	"errors"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	body := `0123abc  agent-telemetry_darwin_arm64.tar.gz
deadbeef  agent-telemetry_linux_amd64.tar.gz
cafef00d  agent-telemetry_darwin_amd64.tar.gz
`
	got, err := parseChecksum(body, "agent-telemetry_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatalf("parseChecksum: %v", err)
	}
	if got != "0123abc" {
		t.Errorf("checksum = %q, want %q", got, "0123abc")
	}

	if _, err := parseChecksum(body, "no-such-asset.tar.gz"); err == nil {
		t.Errorf("expected error for missing asset, got nil")
	}
}

func TestPickURLs(t *testing.T) {
	assets := []releaseAsset{
		{Name: "checksums.txt", BrowserDownloadURL: "https://example/checksums.txt"},
		{Name: "agent-telemetry_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example/asset.tar.gz"},
		{Name: "agent-telemetry_linux_amd64.tar.gz", BrowserDownloadURL: "https://example/other.tar.gz"},
	}
	a, c := pickURLs(assets, "agent-telemetry_darwin_arm64.tar.gz")
	if a != "https://example/asset.tar.gz" {
		t.Errorf("asset url = %q", a)
	}
	if c != "https://example/checksums.txt" {
		t.Errorf("checksums url = %q", c)
	}

	a, _ = pickURLs(assets, "agent-telemetry_windows_arm64.tar.gz")
	if a != "" {
		t.Errorf("expected empty asset url for missing platform, got %q", a)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"v0.0.3":   "0.0.3",
		"0.0.3":    "0.0.3",
		" v0.0.3 ": "0.0.3",
		"":         "",
		"dev":      "dev",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewGitHubRequestAddsBearerToken(t *testing.T) {
	req, err := newGitHubRequest("https://api.github.com/repos/owner/repo/releases/latest", " token-123 \n")
	if err != nil {
		t.Fatalf("newGitHubRequest: %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer token-123" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestNewGitHubRequestSkipsEmptyToken(t *testing.T) {
	req, err := newGitHubRequest("https://api.github.com/repos/owner/repo/releases/latest", " \n")
	if err != nil {
		t.Fatalf("newGitHubRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

func TestResolveGitHubTokenPrefersEnvironment(t *testing.T) {
	calledGh := false
	got := resolveGitHubToken(
		func(name string) string {
			if name != "GITHUB_TOKEN" {
				t.Fatalf("unexpected env lookup %q", name)
			}
			return " env-token "
		},
		func() (string, error) {
			calledGh = true
			return "gh-token", nil
		},
	)
	if got != "env-token" {
		t.Errorf("token = %q, want env-token", got)
	}
	if calledGh {
		t.Errorf("gh token command should not be called when GITHUB_TOKEN is set")
	}
}

func TestResolveGitHubTokenFallsBackToGh(t *testing.T) {
	got := resolveGitHubToken(
		func(string) string { return "" },
		func() (string, error) { return " gh-token\n", nil },
	)
	if got != "gh-token" {
		t.Errorf("token = %q, want gh-token", got)
	}
}

func TestResolveGitHubTokenAllowsUnauthenticatedFallback(t *testing.T) {
	got := resolveGitHubToken(
		func(string) string { return "" },
		func() (string, error) { return "", errors.New("gh unavailable") },
	)
	if got != "" {
		t.Errorf("token = %q, want empty", got)
	}
}
