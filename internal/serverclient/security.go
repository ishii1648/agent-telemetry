package serverclient

import (
	"net"
	"net/url"
	"strings"
)

// InsecureTarget describes a configured export target whose endpoint would POST
// telemetry — and, when HasCredential is set, a bearer token / API key — as
// cleartext `http://` to a non-loopback host (issue 0059). config is
// operator-controlled, so this is a misconfiguration nudge rather than an SSRF
// boundary: a typo'd `http://` leaks user_id / cwd / repo / branch / pr_url
// (and any auth header) onto the wire in the clear. The tokenless localhost
// Collector recipe (issues 0050 / 0051) is the one intentional plaintext case,
// so loopback endpoints are never flagged — see IsInsecurePlaintext.
type InsecureTarget struct {
	ID            string
	Endpoint      string
	HasCredential bool
}

// IsInsecurePlaintext reports whether this target would send cleartext over
// `http://` to a non-loopback host. It is the single policy seam for issue
// 0059: loopback (localhost / 127.0.0.0/8 / ::1) is the intentional tokenless
// local-Collector path and is always allowed; any other `http://` host is
// flagged so the operator can switch to `https://` (or confirm the plaintext
// hop is deliberate). A malformed endpoint or a non-`http` scheme (`https`,
// `grpc`, …) is never flagged here — empty/typo'd endpoints are already
// surfaced as Misconfigured, and we only police the cleartext-leak case.
func (t ExportTarget) IsInsecurePlaintext() bool {
	u, err := url.Parse(t.Endpoint)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	return !isLoopbackHost(u.Hostname())
}

// hasCredential reports whether a non-empty token would ride this request,
// turning a plaintext hop into a credential leak (escalates the warning).
func (t ExportTarget) hasCredential() bool {
	return t.Token != ""
}

// isLoopbackHost reports whether host names the local machine: the literal
// "localhost", or any IP that net considers loopback (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// InsecurePlaintextTargets returns the configured targets that would leak
// cleartext to a non-loopback host. Only Configured() targets are considered:
// an endpoint-less target is an opt-out (or a Misconfigured nudge), not a leak.
func InsecurePlaintextTargets(targets []ExportTarget) []InsecureTarget {
	var out []InsecureTarget
	for _, t := range targets {
		if !t.Configured() || !t.IsInsecurePlaintext() {
			continue
		}
		out = append(out, InsecureTarget{
			ID:            t.ID,
			Endpoint:      t.Endpoint,
			HasCredential: t.hasCredential(),
		})
	}
	return out
}
