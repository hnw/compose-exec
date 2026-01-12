# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[English README](./README.md)

**`os/exec` のように Compose サービスを扱う、Docker CLI 非依存の Go ライブラリ**

`compose-exec` は、`docker-compose.yml` を定義ファイルとして利用し、Go のコードから直接コンテナのライフサイクル（起動・実行・終了）を制御するライブラリです。
`docker` コマンドやシェルスクリプトを一切介さず、Docker Engine API を直接操作するため、安全かつ堅牢にコンテナを管理できます。

```mermaid
graph LR
    classDef host fill:#fafafa,stroke:#666,stroke-width:2px,color:#333;
    classDef container fill:#e3f2fd,stroke:#1565c0,stroke-width:2px,color:#0d47a1;
    classDef daemon fill:#1565c0,stroke:#fff,stroke-width:0px,color:#fff;
    classDef target fill:#fff3e0,stroke:#ef6c00,stroke-dasharray: 5 5,color:#e65100;

    subgraph Host ["ホストマシン"]
        File["docker-compose.yml"]:::host
        Daemon[["Dockerデーモン"]]:::daemon
    end

    subgraph Controller ["Goプロセス<br>(ホストまたはコンテナ)"]
        Lib["compose-exec"]:::container
    end

    Target("ターゲットコンテナ"):::target

    Lib -- "1. 設定の読み込み" --> File
    Lib -- "2. API呼び出し (ソケット)" --> Daemon
    Daemon -- "3. 生成 (DooD)" --> Target

    class Host host;
    class Controller container;

```

## 📖 Usage (Integration Testing)

既存の `docker-compose.yml` を利用して、DBの起動を待機してからテスト処理を実行する例です。
`testcontainers` のように Go コード内でコンテナ定義を書き直す必要はありません。

```go
package main

import (
	"context"
	"fmt"
	"os"
	"github.com/hnw/compose-exec/compose"
)

func main() {
	// 終了時にコンテナを停止させるためのコンテキスト
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. docker-compose.yml から "db" サービスの設定を読み込む
	svc := compose.From("db")

	// 2. コマンド定義（引数なし＝イメージのデフォルトコマンドを使用）
	cmd := svc.Command()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. コンテナを起動 (Start)
	if err := cmd.Start(ctx); err != nil {
		panic(err)
	}

	// 関数終了時に確実にコンテナを削除する
	defer cmd.Wait(ctx)

	// 4. ✨ ヘルスチェック通過を待機
	// docker-compose.yml の healthcheck 定義を使用します。
	// "sleep 10" のような不安定な待機処理は不要です。
	fmt.Println("Waiting for DB to be healthy...")
	if err := cmd.WaitUntilHealthy(ctx); err != nil {
		panic(err)
	}

	// 5. テストやバッチ処理の実行
	fmt.Println("DB is ready! Running tests...")
	// runTests()
}

```

## 🏃 Try it now (Sibling Container Demo)

このリポジトリ自体が動作デモになっています。
以下のコマンドを実行すると、「Go製のコントローラー（コンテナ）」が「兄弟コンテナ（Sibling）」を動的に起動・制御する様子を確認できます。Go のインストールも不要です。

```bash
# クローンして実行するだけ
git clone https://github.com/hnw/compose-exec.git
cd compose-exec
docker compose run controller

```

実行結果ログ (Output)

```text
[Controller] Launching 'Slow-Start' Target Container...
[Controller] 1. Attempting IMMEDIATE connection (Expect FAILURE)...
   -> As expected, connection failed: dial tcp: lookup target: no such host
[Controller] 2. Waiting for Target (Port 8080) to be Ready...
   -> Target is HEALTHY! Waited: 3.2s
[Controller] 3. Connecting to target:8080 ... SUCCESS!

```

このデモは、CI環境（GitHub Actionsなど）で Docker コンテナ内から他のコンテナを操作する DooD (Docker outside of Docker) パターンの実装例としても参照できます。

## ✨ Why compose-exec?

シェルスクリプトや `exec.Command("docker", ...)` と比較したメリット：

* **No Docker Binary Required:**
実行環境に `docker` CLI が不要です。`distroless` や `scratch` ベースの軽量コンテナ内でも動作します。
* **Robust Lifecycle Management:**
Go の `Context` と連動してコンテナを管理します。テストがタイムアウトしたりパニックした場合でも、コンテナは確実に停止・削除され、ゾンビプロセス化を防ぎます。
* **Secure & Injection-Proof:**
シェルを経由せず API を直接叩くため、OS コマンドインジェクションのリスクを構造的に排除しています。ChatOps ボットや、LLM (AI) がコードを実行するためのサンドボックス環境の実装に最適です。

## 🛠 Use Cases

1. **自己完結型の結合テスト (Integration Testing)**
`TestMain` でインフラ（DBやMQ）を立ち上げ、テストを実行し、ティアダウンするまでを `go test` だけで完結させることができます。Makefile は不要です。
2. **AI Agents / ChatOps**
Slack ボットや AI エージェントが、ユーザーのリクエストに応じて安全にタスクを実行する基盤として利用できます。万が一コンテナ内に侵入されても、攻撃者が利用できる `docker` コマンドが存在しません。

## ⚙️ Configuration (DooD Setup)

コンテナ内（CI環境など）でこのライブラリを使用する場合、ホスト側の Docker デーモンを操作するための設定が必要です。

特に **「ミラーマウント（Mirror Mount）」** が重要です。コンテナ内のファイルパスとホスト側のファイルパスを一致させることで、Compose ファイルの相対パス解決やバインドマウントが正しく機能します。

**docker-compose.yml (Controller 側の設定例):**

```yaml
services:
  controller:
    image: golang:1.24
    volumes:
      # 1. Docker API ソケットの共有 (必須)
      - /var/run/docker.sock:/var/run/docker.sock

      # 2. ミラーマウント (必須)
      # ホストのカレントディレクトリ(${PWD})を、コンテナ内の同じパスにマウントする
      - .:${PWD}

    # 3. 作業ディレクトリの同期
    working_dir: ${PWD}

```

## Installation

```bash
go get github.com/hnw/compose-exec

```

## Requirements

* **Go:** 1.24以上
* **Docker Engine:** APIバージョン 1.40以上
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2推奨)

## License

MIT
