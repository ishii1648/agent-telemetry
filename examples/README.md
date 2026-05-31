# examples/

agent-telemetry を踏まえた **自動化ユースケースのリファレンス実装** を置くディレクトリ。

agent-telemetry 本体の責務は「外れ値 PR をダッシュボードで示唆する」ところまで。その先で「外れ値 PR を coding agent に分析させる」「分析結果を Issue / Slack に流す」といった自動化を組みたいユースケースは成立するため、参考になる skill / script を `examples/` に同梱する。

## 位置づけ

- **best-effort**: CI で検証していない。スキーマ変更や CLI 変更に追随しない場合がある
- **コピー前提**: 利用者は中身を読んで自分の環境用に書き換える前提。そのまま動く保証はしない
- **責務分離**: 各 sample は **stdout への出力までで完結** させる。出力先（GitHub Issue / PR コメント / Slack 投稿等）は呼び出し側の責務

## ⚠️ Privacy 注意

`~/.claude/agent-telemetry.db` および transcript JSONL には次のような **機密になりうる情報** が含まれる:

- ローカルパス・ブランチ名・リポジトリ名
- ユーザが入力したプロンプト全文
- AI の応答に含まれるコード断片・コメント・URL
- tool 呼び出しの引数（`Read` / `Edit` の対象ファイルパス、`Bash` のコマンド等）

skill / script の出力を **外部に送信する前に必ずスコープを確認** すること。組織のポリシーで許される範囲か、特定のリポジトリだけに絞るべきか、ユーザ入力を含む箇所を伏せるか等を呼び出し側で判断する。

## サンプル一覧

### `skills/analyze-pr/`

外れ値 PR の transcript を読み、token 消費の外れ値要因と改善仮説を Markdown で stdout に出す Claude Code skill。

```
/analyze-pr <pr_url>
/analyze-pr --worst-by fresh_tokens --limit 5
```

詳細は [`skills/analyze-pr/SKILL.md`](skills/analyze-pr/SKILL.md) を参照。

## 呼び出し例

### 例 1: Claude Code から手動で実行

skill を `~/.claude/skills/analyze-pr/` にコピー（またはシンボリックリンク）してから:

```fish
# 単発
claude /analyze-pr https://github.com/org/repo/pull/123

# 一括（fresh_tokens で降順上位 5 件）
claude /analyze-pr --worst-by fresh_tokens --limit 5
```

stdout を `tee` でファイルに保存しておけば、後続の手動レビューに使える。

### 例 2: Claude Action（GitHub Actions）で週次バッチ

GitHub Actions 上で `agent-telemetry sync-db` してから analyze-pr を呼び、結果を Issue として開く構成。

```yaml
# .github/workflows/agent-telemetry-weekly.yml
on:
  schedule:
    - cron: "0 9 * * MON"  # 毎週月曜 9:00 UTC
  workflow_dispatch:

jobs:
  analyze:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Sync DB
        run: agent-telemetry sync-db
      - name: Analyze worst PRs
        id: analyze
        run: |
          claude /analyze-pr --worst-by fresh_tokens --limit 5 > report.md
      - name: Open Issue
        run: |
          gh issue create \
            --title "Weekly token-efficiency review ($(date +%Y-%m-%d))" \
            --body-file report.md \
            --label agent-telemetry
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

> 注: 上記は雛形。skill を実行できる環境（claude CLI と DB）を Actions 上で用意する手順は省略している。実運用ではセルフホストランナー等での実行を想定する。

### 例 3: Claude Web Routine から日次で Slack へ

Claude Web の Routine に登録し、結果を Slack の特定チャンネルに投げる構成。

1. Routine の prompt に次のような指示を入れる:
   ```
   /analyze-pr --worst-by fresh_tokens --limit 3
   出力された Markdown を要約 100 字以内で Slack の #agent-telemetry に post してください。
   ```
2. Slack の outgoing destination は Web 側のツール連携で接続する
3. **送信前に必ずチャンネルのスコープと内容の機密性を確認** すること（Privacy 注意参照）

`--limit 3` に絞っているのは、Slack に投げる際にノイズを抑えるため。詳細レポートは別経路（GitHub Issue 等）で残し、Slack には要約だけ流すのが運用上扱いやすい。
