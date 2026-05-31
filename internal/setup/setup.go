// Package setup prints agent-specific hook registration instructions.
// It does NOT register hooks — dotfiles or manual editing remain the
// source of truth.
package setup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/configpath"
)

// HookSpec describes one agent-telemetry hook entry.
type HookSpec struct {
	Event      string // hook event name as written in settings (e.g. "SessionStart")
	Subcommand string // agent-telemetry hook subcommand
	Optional   bool   // true → doctor flags as info, not failure
	Async      bool   // true → hook should be registered with "async": true (doctor hints when missing)
}

// ClaudeHookSpecs lists the canonical hooks for Claude Code.
//
// Stop is Async: it fires every response turn, so it must be registered with
// "async": true to keep Claude Code from blocking the user's response cycle on
// the hook process. doctor surfaces a hint when Stop is registered without it.
var ClaudeHookSpecs = []HookSpec{
	{Event: "SessionStart", Subcommand: "session-start"},
	{Event: "SessionEnd", Subcommand: "session-end"},
	{Event: "Stop", Subcommand: "stop", Async: true},
}

// CodexHookSpecs lists the canonical hooks for Codex CLI. PostToolUse is
// optional — Codex is happy without PR URL auto-detection, since `update`
// and Stop-time backfill cover the same data.
var CodexHookSpecs = []HookSpec{
	{Event: "SessionStart", Subcommand: "session-start"},
	{Event: "Stop", Subcommand: "stop"},
	{Event: "PostToolUse", Subcommand: "post-tool-use", Optional: true},
}

// HookSpecsFor returns the hook list for a given agent.
func HookSpecsFor(name string) []HookSpec {
	switch name {
	case agent.NameCodex:
		return CodexHookSpecs
	default:
		return ClaudeHookSpecs
	}
}

// SettingsPath returns the resolved path of ~/.claude/settings.json.
func SettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// CodexHooksPath returns the conventional path for ~/.codex/hooks.json.
//
// Codex supports two equivalent registration mechanisms (TOML in
// config.toml or JSON in hooks.json); doctor inspects both.
func CodexHooksPath() string {
	a := agent.Codex()
	return filepath.Join(a.DataDir, "hooks.json")
}

// CodexConfigPath returns ~/.codex/config.toml.
func CodexConfigPath() string {
	a := agent.Codex()
	return filepath.Join(a.DataDir, "config.toml")
}

// Run prints registration instructions for the requested agent (or all
// agents when a is nil). It writes nothing to disk.
func Run(a *agent.Agent) error {
	return RunWith(os.Stdout, a)
}

// RunWith is the test-friendly variant of Run.
func RunWith(w io.Writer, a *agent.Agent) error {
	if a == nil {
		fmt.Fprintln(w, "setup: agent 別のセットアップ手順を表示します（hook 登録は dotfiles または手動）")
		fmt.Fprintln(w)
		printConfigFile(w)
		fmt.Fprintln(w)
		printClaude(w)
		fmt.Fprintln(w)
		printCodex(w)
		return nil
	}
	switch a.Name {
	case agent.NameCodex:
		printCodex(w)
	default:
		printClaude(w)
	}
	return nil
}

func printConfigFile(w io.Writer) {
	preferred := configpath.Preferred()
	fmt.Fprintf(w, "# config file (%s)\n", preferred)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  user_id 上書きとサーバ送信の opt-in に使う TOML ファイル。")
	fmt.Fprintln(w, "  例:")
	fmt.Fprintln(w, "    user = \"alice@example.com\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    [server]")
	fmt.Fprintln(w, "    endpoint = \"https://telemetry.example.com\"")
	fmt.Fprintln(w, "    token    = \"your-api-token\"")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  旧パス %s にあるファイルもしばらく fallback として読みますが、将来削除予定です。\n", configpath.Legacy())
}

func printClaude(w io.Writer) {
	fmt.Fprintln(w, "# Claude Code (~/.claude/settings.json)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  hook 登録は dotfiles または手動で行ってください。")
	fmt.Fprintln(w, "  検証: agent-telemetry doctor")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  例:")
	fmt.Fprintln(w, "  {")
	fmt.Fprintln(w, "    \"hooks\": {")
	fmt.Fprintln(w, "      \"SessionStart\": [")
	fmt.Fprintln(w, "        {\"matcher\": \"\", \"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook session-start --agent claude\"}]}")
	fmt.Fprintln(w, "      ],")
	fmt.Fprintln(w, "      \"SessionEnd\": [")
	fmt.Fprintln(w, "        {\"matcher\": \"\", \"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook session-end --agent claude\", \"timeout\": 10}]}")
	fmt.Fprintln(w, "      ],")
	fmt.Fprintln(w, "      \"Stop\": [")
	fmt.Fprintln(w, "        {\"matcher\": \"\", \"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook stop --agent claude\", \"async\": true}]}")
	fmt.Fprintln(w, "      ]")
	fmt.Fprintln(w, "    }")
	fmt.Fprintln(w, "  }")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Stop は応答ターンごとに発火するため \"async\": true で登録します")
	fmt.Fprintln(w, "  （Claude Code v2.1.0+。hook プロセスの終了をユーザ応答サイクルが待たない）。")
}

func printCodex(w io.Writer) {
	fmt.Fprintln(w, "# Codex CLI (~/.codex/config.toml or ~/.codex/hooks.json)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Codex には SessionEnd イベントが無いため、Stop hook が SessionEnd を兼ねます。")
	fmt.Fprintln(w, "  hooks.json での例:")
	fmt.Fprintln(w, "  {")
	fmt.Fprintln(w, "    \"hooks\": {")
	fmt.Fprintln(w, "      \"SessionStart\": [")
	fmt.Fprintln(w, "        {\"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook session-start --agent codex\"}]}")
	fmt.Fprintln(w, "      ],")
	fmt.Fprintln(w, "      \"Stop\": [")
	fmt.Fprintln(w, "        {\"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook stop --agent codex\"}]}")
	fmt.Fprintln(w, "      ],")
	fmt.Fprintln(w, "      \"PostToolUse\": [")
	fmt.Fprintln(w, "        {\"hooks\": [{\"type\": \"command\", \"command\": \"agent-telemetry hook post-tool-use --agent codex\"}]}")
	fmt.Fprintln(w, "      ]")
	fmt.Fprintln(w, "    }")
	fmt.Fprintln(w, "  }")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  config.toml 形式で書く場合は [features] codex_hooks = true を有効にしてから")
	fmt.Fprintln(w, "  [[hooks.SessionStart]] / [[hooks.Stop]] を追加してください。")
}
