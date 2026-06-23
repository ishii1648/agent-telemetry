---
title: セットアップ
weight: 20
sidebar:
  open: true
---

agent-telemetry のセットアップと運用手順をまとめます。仕組みの理解から先に始めたい場合は [仕組み解説]({{< relref "/explain" >}}) を先に読むと、各セットアップ手順が「どこに効いているか」を把握しやすくなります。

1. [local]({{< relref "/setup/local" >}}) — ローカルマシンへの導入（CLI・hook 登録・Grafana 設定）
2. [server]({{< relref "/setup/server" >}}) — `agent-telemetry-server` を立てて複数マシン / チームで集計値を集約する（オプトイン）
3. [datadog]({{< relref "/setup/datadog" >}}) — Datadog へ raw events と PR 単位 gauge を送る（direct / collector のどちらかを選ぶ。オプトイン）
