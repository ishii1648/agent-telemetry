# Datadog Logs Pipeline — direct レシピの attribute 整形を IaC 化するサンプル。
#
# 対象は「attribute 整形」層のみ (rename / tag 昇格 / drop)。
# facet / measure 化は Logs Pipeline では実現できない → facets-measures.md。
#
# provider: DataDog/datadog (terraform registry)
# 分類の正本: docs/spec.md「OTLP export の attribute 意味分類」

terraform {
  required_providers {
    datadog = {
      source = "DataDog/datadog"
    }
  }
}

# DD_API_KEY / DD_APP_KEY は環境変数 or provider block で渡す。
provider "datadog" {}

locals {
  # 群 1 の低 cardinality 次元タグのうち raw attribute として載るもの。
  # task_type / is_subagent は raw attribute に無く branch / parent_session_id から
  # 導出するため、ここには含めず下の category_processor で扱う。
  tag_dimensions = [
    "coding_agent", "agent_version", "user_id", "repo",
    "end_reason", "model", "pr_state", "is_merged", "is_ghost",
  ]
}

resource "datadog_logs_custom_pipeline" "agent_telemetry" {
  name       = "agent-telemetry"
  is_enabled = true

  filter {
    # OTLP intake で resource の service.name=agent-telemetry が service tag になる。
    query = "service:agent-telemetry"
  }

  # --- 群 1: 低 cardinality 属性を tag に昇格 -------------------------------
  # attribute_remapper は「複数 sources を単一 target へ統合」する processor なので、
  # 各属性をそれぞれ同名 tag へ昇格するには属性ごとに 1 processor 必要
  # (sources を並べて target を空にすると 1 つの tag に畳まれてしまう)。
  dynamic "processor" {
    for_each = local.tag_dimensions
    iterator = dim
    content {
      attribute_remapper {
        name                 = "promote ${dim.value} to tag"
        is_enabled           = true
        sources              = [dim.value]
        source_type          = "attribute"
        target               = dim.value
        target_type          = "tag"
        preserve_source      = false
        override_on_conflict = false
      }
    }
  }

  # --- 群 2: pr_url を tag に昇格 (cardinality コストに注意) ----------------
  processor {
    attribute_remapper {
      name                 = "promote pr_url (monotonic key) to tag"
      is_enabled           = true
      sources              = ["pr_url"]
      source_type          = "attribute"
      target               = "pr_url"
      target_type          = "tag"
      preserve_source      = true
      override_on_conflict = false
    }
  }

  # --- is_subagent の導出 (raw attribute に無いので parent_session_id から) --
  processor {
    category_processor {
      name   = "derive is_subagent from parent_session_id"
      target = "is_subagent"

      category {
        name = "true"
        filter {
          query = "@parent_session_id:*"
        }
      }
      category {
        name = "false"
        filter {
          query = "-@parent_session_id:*"
        }
      }
    }
  }

  # --- task_type の導出 (raw attribute に無いので branch のプレフィックスから) -
  processor {
    category_processor {
      name   = "derive task_type from branch prefix"
      target = "task_type"

      category {
        name = "feat"
        filter {
          query = "@branch:feat\\/*"
        }
      }
      category {
        name = "fix"
        filter {
          query = "@branch:fix\\/*"
        }
      }
      category {
        name = "docs"
        filter {
          query = "@branch:docs\\/*"
        }
      }
      category {
        name = "chore"
        filter {
          query = "@branch:chore\\/*"
        }
      }
    }
  }

  # 群 3 (session_id / parent_session_id / branch / pr_title) は tag 化しない。
  # 検索 facet 化は facets-measures.md で別途行う (Pipeline の役割外)。
  # 群 4 (数値 measure) も Pipeline では昇格不可 → facets-measures.md。
}
