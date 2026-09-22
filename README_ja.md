# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[English README](./README.md)

`compose-exec` は、コンテナ化されたツールを Go から外部コマンドのように呼び出すためのライブラリです。

ツールをアプリケーションイメージに組み込まず、それぞれを Compose サービスとして分離したまま、`os/exec` に近い API で実行できます。

```go
cmd := compose.CommandContext(ctx, "pandoc", "input.md", "-t", "html")
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr

if err := cmd.Run(); err != nil {
	log.Fatal(err)
}
```

イメージ、volume、環境変数、network など、ツール固有の実行条件は `compose.yaml` にまとめておけます。

`compose-exec` は `docker compose` を起動せず、Docker Engine を直接操作します。

```mermaid
flowchart LR
    Go["Go program"]
    CE["compose-exec"]
    CLI["docker compose"]
    Docker["Docker Engine"]
    Service["Compose service"]

    Go --> CE --> Docker --> Service
    CLI -.-> Docker
```

## Example

このリポジトリには Pandoc を使った実行例があります。

デモは2つのサービスで構成されています。

* `controller`: `compose-exec` を使う Go プログラムを実行する
* `pandoc`: Pandoc を別コンテナとして提供する

Compose のサービスは、`docker compose up` で常駐させるだけでなく、`docker compose run` のようにサービス定義を使って一時コンテナを起動する用途にも使えます。

`compose-exec` も同じ考え方で、Go プログラムから `pandoc` を呼び出したときに、`pandoc` サービスの定義を使ってコンテナを起動し、指定したコマンドを実行します。

次のコマンドで実行できます。

```bash
git clone https://github.com/hnw/compose-exec.git
cd compose-exec
docker compose run --rm controller
```

実行例:

```text
[Controller] Converting Markdown to HTML...
[Controller] Running Pandoc via the "pandoc" Compose service.

Input: example/input.md

<h1 id="hello-pandoc">Hello, Pandoc</h1>
<p>This Markdown file is converted to HTML by the
<strong>pandoc</strong> Compose service.</p>
...

[Controller] Done. Pandoc ran in a separate container,
[Controller] so it is not installed in the controller image.
```

`pandoc` サービスは `compose.yaml` で定義されています。

```yaml
services:
  pandoc:
    image: pandoc/core:3.11.0.0
    volumes:
      - ./example:/data
    working_dir: /data
```

Pandoc 本体や依存関係を `controller` イメージに組み込む必要はありません。Pandoc のバージョンも image tag を変更するだけで個別に更新できます。

ローカルに Go がインストールされていれば、同じプログラムをホストから直接実行することもできます。

```bash
go run ./example
```

この場合、Go プログラムはホスト上で動作し、Pandoc だけがコンテナで実行されます。

## Why compose-exec?

`compose-exec` は、Go プログラムから1つ以上のツールをコンテナで実行し、その実行環境を Compose 側で管理したい場合に向いています。

例えば、次のような用途に使えます。

* ツール本体や依存関係を、それぞれ別のコンテナイメージに分離する
* ツールのバージョンをアプリケーションとは独立して更新する
* stdin、stdout、stderr、`context.Context` を `os/exec` に近い API で扱う

複数のコンテナ化されたツールを呼び出す、ボット、エージェント、CI用ヘルパー、自動化サービスなどでは特に使いやすい構成です。

## Usage

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/hnw/compose-exec/compose"
)

func main() {
	ctx := context.Background()

	cmd := compose.CommandContext(ctx, "pandoc", "input.md", "-t", "html")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}
```

`Command()` と `CommandContext()` は、カレントディレクトリから Compose project を読み込みます。

同じ project から複数のコマンドを実行する場合は、一度だけ読み込んで再利用できます。

```go
project, err := compose.LoadProject(ctx, ".")
if err != nil {
	log.Fatal(err)
}

cmd := project.CommandContext(ctx, "tool", "--version")
```

## Docker-outside-of-Docker

Go プログラム自体をコンテナ内で実行し、ホストの Docker daemon を利用することもできます。

この構成では、`controller` はマウントした Docker socket を使い、`compose.yaml` のサービス定義から sibling container を起動します。

Docker socket をマウントし、project directory はホストと `controller` コンテナ内で同じ絶対パスに配置します。

```yaml
services:
  controller:
    image: golang:1.25
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - .:${PWD}
    working_dir: ${PWD}
    environment:
      - PWD=${PWD}
```

`compose.yaml` の bind mount が正しいホスト側のパスを参照できるよう、project directory のパスを揃える必要があります。

## Compose support

`compose-exec` は、コンテナ化されたツールや関連サービスを実行する際によく使う Compose の設定に対応しています。

主な制限事項:

* `build` には対応していません。サービスには `image` の指定が必要です
* Docker Compose の完全な実装ではありません
* TTY には対応していません

対応しているサービス設定:

* `image`, `platform`
* `command`, `entrypoint`, `working_dir`
* `environment`, `env_file`
* `volumes`, `tmpfs`, `read_only`
* `ports`
* `networks`, `network_mode`, `extra_hosts`
* `healthcheck`
* `user`, `init`
* `stop_signal`, `stop_grace_period`
* `privileged`, `cap_add`, `cap_drop`, `security_opt`
* `shm_size`, `devices`
* `mem_limit`, `mem_reservation`, `memswap_limit`
* `cpus`, `cpu_shares`, `cpuset`
* `ulimits`, `labels`

上記以外の Compose 設定はサポート対象外です。

## Installation

```bash
go get github.com/hnw/compose-exec
```

## Requirements

* Go 1.24 以降
* Docker Engine API v1.40 以降

## License

MIT
