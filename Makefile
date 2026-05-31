.PHONY: build install uninstall intent test-intent docs-serve docs-build docs-mod-update

PREFIX ?= $(HOME)/.local
BIN_DIR := $(PREFIX)/bin
BIN_NAME := agent-telemetry

build:
	CGO_ENABLED=0 go build -o bin/$(BIN_NAME) ./cmd/agent-telemetry/

install:
	@mkdir -p "$(BIN_DIR)"
	CGO_ENABLED=0 go build -o "$(BIN_DIR)/$(BIN_NAME)" ./cmd/agent-telemetry/
	@echo "Installed: $(BIN_DIR)/$(BIN_NAME)"
	@case ":$$PATH:" in *":$(BIN_DIR):"*) ;; *) echo "Warning: $(BIN_DIR) is not in PATH";; esac

uninstall:
	rm -f "$(BIN_DIR)/$(BIN_NAME)"
	@echo "Removed: $(BIN_DIR)/$(BIN_NAME)"

# 可視化は otel+grafana(Mimir/Loki) に一本化した（issues/0055）。SQLite を Grafana
# datasource として直読みする経路（旧 grafana-up / grafana-screenshot / lint-dashboard）
# は撤去済み。otel backend のローカル検証は deploy/oss-observability/ の compose を使う。

# code path から関連 issue / commit action 行を逆引きする dev tool（goreleaser には含めない）。
# path → issue は issue 本文の grep と git 履歴から動的に求める（手書きインデックス不要）。
# 変数名は P= を使う（PATH= は Make が実行時 PATH と解釈してしまうため）。
intent:
	@if [ -z "$(P)" ]; then \
		echo "Usage: make intent P=<path> [FORMAT=markdown|json] [FULL=1]"; \
		echo "  e.g. make intent P=internal/hook/stop.go"; \
		echo "       make intent P=internal/syncdb/ FORMAT=json"; \
		echo "       make intent P=internal/hook/stop.go FULL=1"; \
		exit 2; \
	fi
	@scripts/intent-lookup $(P) $(if $(FORMAT),--format=$(FORMAT),) $(if $(FULL),--full,)

test-intent:
	@python3 scripts/test_intent_lookup.py

# Hugo docs site (site/) — ローカル確認とビルド
# 既定の port 1313 を Grafana 等と衝突させたい場合は HUGO_PORT で上書き。
HUGO_PORT ?= 1313

docs-serve:
	cd site && hugo mod get -u ./... || true
	cd site && hugo server --buildDrafts --port $(HUGO_PORT)

docs-build:
	cd site && hugo mod get -u ./...
	cd site && hugo --gc --minify

# 依存 module（theme 等）を最新化して go.sum を refresh
docs-mod-update:
	cd site && hugo mod get -u ./...
	cd site && hugo mod tidy
