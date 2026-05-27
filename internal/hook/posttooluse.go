package hook

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

// prURLRe matches a GitHub PR URL anywhere in a text blob. The caller gates
// this to `gh pr create` output; owner / repo / number are reconstructed
// downstream.
var prURLRe = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/pull/\d+`)

// RunPostToolUse handles the PostToolUse hook event. In the supported hook
// setup this is registered for Codex, where it scans `gh pr create` output
// for newly created PR URLs and appends them to session-index.jsonl so
// backfill can attach merge metadata next run.
//
// Failure to find a URL is normal (most tools are not PR creation) and
// must not surface as an error — that would break Codex's hook chain on
// every Bash call.
func RunPostToolUse(input *HookInput, a *agent.Agent) error {
	if a == nil {
		a = agent.Claude()
	}
	if input == nil || input.SessionID == "" {
		return nil
	}

	if !isGHPRCreateCommand(input.ToolInput) {
		return nil
	}

	urls := extractPRURLs(input.ToolResponse)
	if len(urls) == 0 {
		return nil
	}
	_, err := sessionindex.Update(a.SessionIndexPath(), input.SessionID, urls)
	return err
}

// extractPRURLs scans a JSON blob (the tool_response payload) for any
// GitHub PR URLs. Both string and structured payloads are covered: we
// stringify the raw JSON and regex over the result, which is cheap and
// agnostic to the exact tool that produced the output.
func extractPRURLs(payload json.RawMessage) []string {
	if len(payload) == 0 {
		return nil
	}
	matches := prURLRe.FindAllString(string(payload), -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, u := range matches {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func isGHPRCreateCommand(payload json.RawMessage) bool {
	if len(payload) == 0 {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return false
	}
	fields := strings.Fields(in.Command)
	return len(fields) >= 3 && fields[0] == "gh" && fields[1] == "pr" && fields[2] == "create"
}
