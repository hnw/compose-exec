# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)

[English README is here](./README.md)

**セキュアでポータブルな DevOps のためのミッシングリンク。**

`compose-exec` は、Docker Compose サービス内でコマンドを実行するための、外部依存ゼロ（Dependency-Free）の Go ライブラリです。
`os/exec` と完全に互換性のあるインターフェースを提供し、**Sibling Container Pattern（兄弟コンテナパターン）** を最もセキュアかつ堅牢に実装します。

## ⚡ Why compose-exec?

従来の「コンテナ内に Docker CLI をインストールする」アプローチは、セキュリティとポータビリティの観点で問題を抱えています。本ライブラリは、現代の DevOps が抱える課題に対する論理的な回答です。

### 1. Zero-Trust & Distroless Ready (セキュリティ)
攻撃対象領域（Attack Surface）を極限まで縮小します。
* **No Docker CLI:** コンテナ内に `docker` バイナリや `docker-compose` コマンドをインストールする必要はありません。
* **No Shell:** `/bin/sh` すら不要です。
* これにより、**[Distroless](https://github.com/GoogleContainerTools/distroless)** (static) や `scratch` イメージでの動作が可能となります。攻撃者が悪用できるツールが一切存在しない環境で、コンテナオーケストレーションを実現できます。

### 2. Pipeline as Code (ポータビリティ)
CI/CD パイプラインのロジックを、特定のベンダー（GitHub Actions, GitLab CI）の YAML 設定から解放します。
* **Portable Logic:** ビルド、テスト、デプロイの手順を Go コードとして記述できます。
* **再現性:** 「ローカルでは動くが CI で落ちる」現象を根絶します。Go と Docker がある環境であれば、MacBook 上でも CI ランナー上でも、1ビットも狂わず同じロジックでパイプラインが動作します。

### 3. Native Go Experience (使い勝手)
* **学習コストゼロ:** API は `os/exec` と完全に同じです。
* **堅牢性:** シグナル（SIGINT/SIGTERM）の転送、ゾンビプロセス防止（PID 1 問題）、Exit Code の伝播をライブラリ側で自動的にハンドリングします。

---

## 🚀 Quick Start

### Installation

```bash
go get [github.com/hnw/compose-exec](https://github.com/hnw/compose-exec)

```

### Usage

コードの書き味は `os/exec` と全く同じです。

```go
package main

import (
	"context"
	"os"
	"[github.com/hnw/compose-exec/compose](https://github.com/hnw/compose-exec/compose)"
)

func main() {
	ctx := context.Background()

	// 1. ターゲットとなるサービスを指定 (docker-compose.yml のサービス名)
	// 画像のプル、コンテナの生成、ネットワーク接続は自動的に行われます
	cmd := compose.From("sibling-target").Command("ls", "-la", "/app")

	// 2. パイプを接続
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. 実行
	// 完了後、コンテナは自動的にクリーンアップされます
	if err := cmd.Run(ctx); err != nil {
		// Exit Code も正しく伝播します
		panic(err)
	}
}

```

---

## 💡 Real-World Use Cases

本ライブラリの真価は、単なるコマンド実行ではなく「インフラのコード化」にあります。

### Scenario A: 自己完結型結合テスト (Self-Contained Integration Tests)

テストコード自身が、必要なインフラ（DB, Cache等）のライフサイクルを管理します。CI の YAML 設定で `services` を定義する必要はありません。

```go
func TestWithDatabase(t *testing.T) {
    // テスト開始時に、必要なDBバージョンをオンデマンドで起動
    // CI環境に依存せず、どこでもテストが実行可能になります
    dbCmd := compose.From("postgres").Command("docker-entrypoint.sh", "postgres")
    dbCmd.Start(context.Background())
    defer dbCmd.Wait() // テスト終了と共に破棄

    // ... テストロジック ...
}

```

### Scenario B: ポータブルな CI/CD ランナー

複雑なビルドフロー（フロントエンドのビルド → 静的アセット化 → Goバイナリへの埋め込み）を Go で記述し、カスタムランナーとして実行します。

* 開発者のマシンに Node.js や npm をインストールする必要はありません。
* `compose-exec` が必要なバージョンの Node.js コンテナを一時的に呼び出し、成果物だけを生成させます。

---

## ⚙️ Configuration

コンテナ内部から `compose-exec` を利用する場合（Sibling Container Pattern）、`docker-compose.yml` に以下の構成が必要です。

### 1. Mirror Mount (`.:${PWD}`)

Docker の Bind Mount は常にホスト OS のパスを基準とします。相対パスを正しく解決させるため、ホストのカレントディレクトリをコンテナ内の**全く同じパス**にマウントする必要があります。

### 2. Manual Profile

ターゲットとなるコンテナ（Sibling）が、`docker compose up` で勝手に立ち上がらないよう、プロファイルを設定します。

### Recommended `docker-compose.yml`

```yaml
services:
  # 1. Controller (あなたの Go アプリ / CI ランナー)
  #    Distroless イメージを使用可能です
  controller:
    image: gcr.io/distroless/static-debian12:latest
    volumes:
      # 必須: Docker API へのアクセス権限
      - /var/run/docker.sock:/var/run/docker.sock
      # 必須: Mirror Mount
      # ホストのカレントディレクトリをコンテナ内の同位置にマップ
      - .:${PWD}
    working_dir: ${PWD}
    # オプション: ホスト上のファイル権限問題を回避するためにユーザーIDを注入
    user: "${UID}:${GID}"
    command: ["/path/to/your-go-binary"]

  # 2. Target Sibling (呼び出される側のコンテナ)
  sibling-target:
    image: alpine:latest
    profiles:
      - manual # compose-exec から呼ばれるまで起動しない

```

---

## ⚠️ Requirements & Compatibility

* **Go:** 1.22+
* **Docker Engine:** API version 1.40+
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2 recommended)

### Note on Image Size

本ライブラリは Docker SDK を内包するため、コンパイル後のバイナリサイズが増加します。
しかし、`docker` CLI バイナリ (約50MB) やシェル環境を含むベース OS イメージ (Alpine等) を同梱する必要がなくなるため、**トータルのコンテナイメージサイズは劇的に削減されます。**

---

## License

MIT
