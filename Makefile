.PHONY: build install uninstall grafana-up grafana-down grafana-flush grafana-screenshot lint-dashboard intent test-intent docs-serve docs-build docs-mod-update

PREFIX ?= $(HOME)/.local
BIN_DIR := $(PREFIX)/bin
BIN_NAME := agent-telemetry

# ローカル可視化スタック（otel: Collector -> Mimir/Loki -> Grafana）。
# ローカル可視化はこの otel 経路に一本化済み（issue 0057）。SQLite を Grafana
# datasource として直接 mount する旧構成（旧 root docker-compose.yaml +
# frser-sqlite-datasource）は撤去し、`grafana-*` ターゲットは otel スタックを指す。
# サーバ k8s 経路の SQLite sidecar は別系統で残置する（site/content/setup/server,
# issues/closed/0029・0030）。
GRAFANA_COMPOSE         ?= deploy/oss-observability/docker-compose.yaml
GRAFANA_PORT            ?= 13001
COMPOSE_PROJECT_GRAFANA ?= agent-telemetry-oss

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

# otel ローカル可視化スタックを 1 コマンドで起動する。
#   Collector(:4318) -> Mimir/Loki -> Grafana(:$(GRAFANA_PORT))
# ローカル可視化の第一級導線（issue 0055 ① / 0057 cutover）。
grafana-up:
	GRAFANA_PORT=$(GRAFANA_PORT) \
	    docker compose -f $(GRAFANA_COMPOSE) -p $(COMPOSE_PROJECT_GRAFANA) up -d
	@echo "Grafana (otel): http://localhost:$(GRAFANA_PORT)  (anonymous admin)"
	@echo "Collector OTLP/HTTP: http://localhost:4318"
	@echo "Export hook data: make grafana-flush"
	@echo "First time: cp deploy/oss-observability/config.toml.example ~/.config/agent-telemetry/config.toml"

grafana-down:
	-docker compose -f $(GRAFANA_COMPOSE) -p $(COMPOSE_PROJECT_GRAFANA) down

# 現在のツリーをビルド（古い導入済みバイナリに引っ張られない）してから
# hook データをスタックへ流す。flush は VIEW を作らないので sync-db を先に
# 走らせて pr_metrics VIEW と PR 集約を最新化する。
grafana-flush: build
	bin/$(BIN_NAME) sync-db
	bin/$(BIN_NAME) flush

# otel dashboard の決定的スクショ (issue 0055 ⑤)。fixture を HOME サンドボックス
# flush で Collector→Mimir/Loki に投入し、Grafana /render で撮る。README ヒーロー
# (docs/assets/dashboard-full.png) はこのターゲットが owner。スクショ専用 stack を
# `make grafana-up` と別 port・別 project で立てるので並走を壊さない。スクリプトが
# build / fixture 生成 / compose 起動 / flush / render / down -v まで一括で行う。
grafana-screenshot:
	bash e2e/oss-screenshot.sh

DASHBOARD_LINTER_VERSION ?= v0.1.0
DASHBOARD_LINTER_DIR     := .cache/dashboard-linter
DASHBOARD_LINTER_BIN     := $(DASHBOARD_LINTER_DIR)/dashboard-linter

# v0.1.0 の go.mod に replace directive が含まれており、Go 1.25 以降の `go run pkg@version`
# / `go install pkg@version` ではビルド不能 (replace を持つ依存はメインモジュールでのみ
# 解釈される)。ソースを直接 clone してビルドし、生成バイナリを呼ぶ方式に切り替える。
$(DASHBOARD_LINTER_BIN):
	@mkdir -p $(DASHBOARD_LINTER_DIR)
	rm -rf $(DASHBOARD_LINTER_DIR)/src
	git clone --depth=1 --branch $(DASHBOARD_LINTER_VERSION) \
	    https://github.com/grafana/dashboard-linter $(DASHBOARD_LINTER_DIR)/src
	cd $(DASHBOARD_LINTER_DIR)/src && go build -o ../dashboard-linter ./

lint-dashboard: $(DASHBOARD_LINTER_BIN)
	$(DASHBOARD_LINTER_BIN) lint --strict --config grafana/dashboards/.lint grafana/dashboards/agent-telemetry.json

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
