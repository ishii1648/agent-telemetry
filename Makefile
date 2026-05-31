.PHONY: build install uninstall grafana-fixtures grafana-up grafana-up-e2e grafana-down grafana-screenshot lint-dashboard intent test-intent docs-serve docs-build docs-mod-update

PREFIX ?= $(HOME)/.local
BIN_DIR := $(PREFIX)/bin
BIN_NAME := agent-telemetry

# 実データ表示用 DB パス（grafana-up が参照）。上書き可。
AGENT_TELEMETRY_DB ?= $(HOME)/.claude/agent-telemetry.db

# grafana-up（実データ）と grafana-up-e2e（fixture）を並行起動できるよう、
# compose project とポートを分離する。互いに独立したスタックとして扱われ、
# 片方を立ち上げてももう片方のコンテナを巻き込まない。
# 既定ポートは ssh トンネル等でよく使われる 13000 / 13001 を避けて 13010+ に置く。
GRAFANA_PORT         ?= 13010
GRAFANA_E2E_PORT     ?= 13011
COMPOSE_PROJECT_REAL ?= agent-telemetry-real
COMPOSE_PROJECT_E2E  ?= agent-telemetry-e2e

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

grafana-fixtures:
	CGO_ENABLED=0 GOTOOLCHAIN=local go test -run TestGenTestDB -v ./e2e/

grafana-up:
	@if [ ! -f "$(AGENT_TELEMETRY_DB)" ]; then \
		echo "DB not found: $(AGENT_TELEMETRY_DB)"; \
		echo "Run 'agent-telemetry sync-db' first, or override: make grafana-up AGENT_TELEMETRY_DB=/path/to/db"; \
		exit 1; \
	fi
	AGENT_TELEMETRY_DB=$(AGENT_TELEMETRY_DB) GRAFANA_PORT=$(GRAFANA_PORT) \
	    docker compose -p $(COMPOSE_PROJECT_REAL) up -d
	@echo "Waiting for Grafana to be ready..."
	@for i in $$(seq 1 60); do \
		if curl -sf http://localhost:$(GRAFANA_PORT)/api/health > /dev/null 2>&1; then \
			echo "Grafana is ready at http://localhost:$(GRAFANA_PORT)"; \
			echo "Showing data from: $(AGENT_TELEMETRY_DB)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "Grafana failed to start within 60s"; exit 1

grafana-up-e2e: grafana-fixtures
	AGENT_TELEMETRY_DB=$(CURDIR)/e2e/testdata/agent-telemetry.db GRAFANA_PORT=$(GRAFANA_E2E_PORT) \
	    docker compose -p $(COMPOSE_PROJECT_E2E) up -d
	@echo "Waiting for Grafana to be ready..."
	@for i in $$(seq 1 60); do \
		if curl -sf http://localhost:$(GRAFANA_E2E_PORT)/api/health > /dev/null 2>&1; then \
			echo "Grafana is ready at http://localhost:$(GRAFANA_E2E_PORT) (e2e fixtures)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "Grafana failed to start within 60s"; exit 1

grafana-down:
	-docker compose -p $(COMPOSE_PROJECT_REAL) down
	-docker compose -p $(COMPOSE_PROJECT_E2E) down

grafana-screenshot: grafana-up-e2e
	GRAFANA_PORT=$(GRAFANA_E2E_PORT) bash e2e/screenshot.sh .outputs/grafana-screenshots

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
