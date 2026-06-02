package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/backfill"
	"github.com/ishii1648/agent-telemetry/internal/configpath"
	"github.com/ishii1648/agent-telemetry/internal/doctor"
	"github.com/ishii1648/agent-telemetry/internal/hook"
	"github.com/ishii1648/agent-telemetry/internal/legacy"
	"github.com/ishii1648/agent-telemetry/internal/serverclient"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
	"github.com/ishii1648/agent-telemetry/internal/setup"
	"github.com/ishii1648/agent-telemetry/internal/syncdb"
)

// version is overwritten at build time via -ldflags "-X main.version=<tag>".
// goreleaser sets this from the git tag; `go build` without ldflags leaves "dev".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "update":
		runUpdate(os.Args[2:])
	case "backfill":
		runBackfill(os.Args[2:])
	case "sync-db":
		runSyncDB(os.Args[2:])
	case "flush":
		runFlush(os.Args[2:])
	case "migrate-to-events":
		runMigrateToEvents(os.Args[2:])
	case "setup":
		runSetup(os.Args[2:])
	case "doctor":
		runDoctor()
	case "hook":
		runHook(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("agent-telemetry %s\n", version)
	case "help", "--help", "-h":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: agent-telemetry <command> [args...]

Commands:
  setup [--agent <claude|codex>]         セットアップ案内を表示（hook 登録は dotfiles または手動）
  doctor                                 検出された agent ごとに binary / data dir / hook 登録を検証（自動修復はしない）
  backfill [--recheck] [--gc] [--detach] [--agent <a>]
                                         検出された agent すべての pr_urls / is_merged / review_comments を補完
  sync-db [--agent <a>]                  検出された agent すべての JSONL/transcript → SQLite 変換（毎回フル再構築）
	  flush [--since-last|--full] [--dry-run] [--agent <a>]
	                                         events を OTLP/HTTP Logs として [server] へ送信（オプトイン）
	  migrate-to-events [--agent <a>]        session-index / transcript から events DB を再生成
  update <session_id> <url>...           session-index.jsonl に PR URL を追加（重複排除）
  update --mark-checked <session_id>...  backfill_checked フラグをセット
  update --by-branch <repo> <branch> <url>  同一 repo+branch の全セッションに URL を追加
  hook <event> [--agent <a>]             hook サブコマンド（settings.json / config.toml / hooks.json から呼ばれる）
    session-start                        セッションメタデータを記録
    session-end                          セッション終了時刻を記録（Claude のみ）
    stop                                 ended_at を記録し backfill worker を非同期起動
    post-tool-use                        tool_response から PR URL を抽出（Codex 用）
    pre-tool-use                         per-session tool 注釈を記録（Claude のみ）
  version                                version を表示
  help                                   このヘルプを表示

Agent precedence: --agent → $AGENT_TELEMETRY_AGENT → autodetect (~/.claude / ~/.codex)`)
}

// extractAgentFlag pulls "--agent <name>" out of args, returning the name
// and the remaining args. Order is preserved for non-flag arguments.
func extractAgentFlag(args []string) (string, []string) {
	out := args[:0:0]
	name := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--agent" && i+1 < len(args):
			name = args[i+1]
			i++
		case len(args[i]) > len("--agent=") && args[i][:len("--agent=")] == "--agent=":
			name = args[i][len("--agent="):]
		default:
			out = append(out, args[i])
		}
	}
	return name, out
}

// runUpdate dispatches `update` to every detected agent's session-index.
// session_id is a UUID and the user shouldn't have to remember which agent
// owns it — we apply the operation to each present index and let the
// no-match branches return cleanly.
func runUpdate(args []string) {
	agentName, args := extractAgentFlag(args)
	agents, err := agent.ResolveOrDetect(agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		os.Exit(1)
	}

	if len(args) == 0 {
		return
	}

	if args[0] == "--mark-checked" {
		sessionIDs := args[1:]
		if len(sessionIDs) == 0 {
			return
		}
		for _, a := range agents {
			if _, err := sessionindex.MarkChecked(a.SessionIndexPath(), sessionIDs); err != nil {
				fmt.Fprintf(os.Stderr, "mark-checked[%s] error: %v\n", a.Name, err)
				os.Exit(1)
			}
		}
		return
	}

	if args[0] == "--by-branch" {
		if len(args) < 4 {
			return
		}
		repo, branch, url := args[1], args[2], args[3]
		for _, a := range agents {
			if _, err := sessionindex.UpdateByBranch(a.SessionIndexPath(), repo, branch, url); err != nil {
				fmt.Fprintf(os.Stderr, "by-branch[%s] error: %v\n", a.Name, err)
				os.Exit(1)
			}
		}
		return
	}

	if len(args) < 2 {
		return
	}
	sessionID := args[0]
	urls := args[1:]
	for _, a := range agents {
		if _, err := sessionindex.Update(a.SessionIndexPath(), sessionID, urls); err != nil {
			fmt.Fprintf(os.Stderr, "update[%s] error: %v\n", a.Name, err)
			os.Exit(1)
		}
	}
}

func runBackfill(args []string) {
	migrateLegacy()
	agentName, args := extractAgentFlag(args)
	opts := backfill.Options{}
	for _, a := range args {
		switch {
		case a == "--recheck":
			opts.Recheck = true
		case a == "--detach":
			opts.DetachedRun = true
		case a == "--gc":
			opts.GCOnly = true
		case len(a) > len("--pin-session=") && a[:len("--pin-session=")] == "--pin-session=":
			opts.PinSessionID = a[len("--pin-session="):]
		}
	}
	if opts.DetachedRun {
		if err := spawnDetachedBackfill(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "backfill detach: %v\n", err)
			os.Exit(1)
		}
		return
	}
	opts.DetachedRun = hasArg(args, "--worker")
	agents, err := agent.ResolveOrDetect(agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backfill: %v\n", err)
		os.Exit(1)
	}
	if err := backfill.RunForAgentsWithOptions(agents, opts); err != nil {
		fmt.Fprintf(os.Stderr, "backfill error: %v\n", err)
		os.Exit(1)
	}
	if opts.DetachedRun {
		if err := syncdb.RunForAgents(agents, syncdb.DBPath()); err != nil {
			fmt.Fprintf(os.Stderr, "sync-db error: %v\n", err)
			os.Exit(1)
		}
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func spawnDetachedBackfill(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	childArgs := []string{"backfill", "--worker"}
	for _, a := range args {
		if a == "--detach" {
			continue
		}
		childArgs = append(childArgs, a)
	}
	cmd := exec.Command(exe, childArgs...)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func runSyncDB(args []string) {
	migrateLegacy()
	agentName, _ := extractAgentFlag(args)
	agents, err := agent.ResolveOrDetect(agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync-db: %v\n", err)
		os.Exit(1)
	}
	if err := syncdb.RunForAgents(agents, syncdb.DBPath()); err != nil {
		fmt.Fprintf(os.Stderr, "sync-db error: %v\n", err)
		os.Exit(1)
	}
}

func runFlush(args []string) {
	migrateLegacy()
	agentName, args := extractAgentFlag(args)
	opts := serverclient.FlushOptions{
		ClientVersion: version,
	}
	for _, a := range args {
		switch a {
		case "--since-last":
			opts.SinceLast = true
		case "--full":
			opts.Full = true
		case "--dry-run":
			opts.DryRun = true
		default:
			fmt.Fprintf(os.Stderr, "flush: unknown flag %q\n", a)
			os.Exit(1)
		}
	}
	if !opts.Full && !opts.SinceLast {
		opts.SinceLast = true
	}
	opts.AgentName = agentName

	res, err := serverclient.RunFlush(context.Background(), opts)
	if res != nil {
		res.Summarize(os.Stderr)
		for _, ar := range res.PerAgent {
			if ar.NoConfig {
				fmt.Fprintf(os.Stderr, "flush: export target（[server] または [[export]]）が %s に未設定です。docs/spec.md ## サーバ送信 を参照してください。\n", configpath.Preferred())
				break
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flush error: %v\n", err)
		os.Exit(1)
	}
}

func runMigrateToEvents(args []string) {
	migrateLegacy()
	agentName, _ := extractAgentFlag(args)
	agents, err := agent.ResolveOrDetect(agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate-to-events: %v\n", err)
		os.Exit(1)
	}
	if err := syncdb.RunForAgents(agents, syncdb.DBPath()); err != nil {
		fmt.Fprintf(os.Stderr, "migrate-to-events error: %v\n", err)
		os.Exit(1)
	}
}

// migrateLegacy renames any hitl-metrics-era files left over in
// ~/.claude / ~/.codex to their agent-telemetry counterparts. Runs
// before commands that read or write these paths so users on the old
// layout don't have to perform a manual migration.
func migrateLegacy() {
	moved, errs := legacy.Migrate()
	legacy.Report(os.Stderr, moved, errs)
}

func runSetup(args []string) {
	agentName, _ := extractAgentFlag(args)
	var a *agent.Agent
	if agentName != "" {
		var err error
		a, err = agent.ByName(agentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			os.Exit(1)
		}
	}
	if err := setup.Run(a); err != nil {
		fmt.Fprintf(os.Stderr, "setup error: %v\n", err)
		os.Exit(1)
	}
}

func runDoctor() {
	r, err := doctor.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor error: %v\n", err)
		os.Exit(1)
	}
	if r.HasFailure() {
		os.Exit(1)
	}
}

func runHook(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-telemetry hook <event-name> [--agent <claude|codex>]")
		os.Exit(1)
	}

	eventName := args[0]
	agentName, _ := extractAgentFlag(args[1:])
	a, err := agent.Resolve(agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook: %v\n", err)
		os.Exit(1)
	}

	var hookErr error
	switch eventName {
	case "session-start":
		input, e := hook.ReadInput()
		if e != nil {
			fmt.Fprintf(os.Stderr, "hook input error: %v\n", e)
			os.Exit(1)
		}
		hookErr = hook.RunSessionStart(input, a)
	case "session-end":
		input, e := hook.ReadInput()
		if e != nil {
			fmt.Fprintf(os.Stderr, "hook input error: %v\n", e)
			os.Exit(1)
		}
		hookErr = hook.RunSessionEnd(input, a)
	case "pre-tool-use":
		input, e := hook.ReadInput()
		if e != nil {
			fmt.Fprintf(os.Stderr, "hook input error: %v\n", e)
			os.Exit(1)
		}
		hookErr = hook.RunPreToolUse(input)
	case "post-tool-use":
		input, e := hook.ReadInput()
		if e != nil {
			fmt.Fprintf(os.Stderr, "hook input error: %v\n", e)
			os.Exit(1)
		}
		hookErr = hook.RunPostToolUse(input, a)
	case "stop":
		// Stop hook reads input only when needed (Codex updates ended_at)
		// but Claude historically called stop with no stdin. Be tolerant
		// of either: try to read but don't fail on EOF / empty stdin.
		input, _ := hook.ReadInput()
		hookErr = hook.RunStop(input, a)
	default:
		fmt.Fprintf(os.Stderr, "unknown hook event: %s\n", eventName)
		os.Exit(1)
	}

	if hookErr != nil {
		fmt.Fprintf(os.Stderr, "hook error: %v\n", hookErr)
		os.Exit(1)
	}
}
