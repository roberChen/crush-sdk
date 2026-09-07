# crush-sdk

Go client SDK for [Crush](https://github.com/charmbracelet/crush) 的 client-server 模式。

Crush 本身支持客户端/服务器架构（`crush server` 提供无头后端，TUI 作为客户端通过
HTTP-over-socket 连接），但其客户端 SDK 一直藏在 `internal/client` 里，外部程序无法引用。
本仓库把客户端提取成独立的 Go module，让编辑器、脚本、自定义 TUI、自动化 agent 等第三方
程序可以直接驱动一个 Crush server。

```go
import "github.com/roberChen/crush-sdk"
```

包名为 `client`。

## 特性

- 与 Crush 官方 TUI 客户端完全相同的 `/v1/...` JSON-over-HTTP 协议
- 支持 Unix socket、TCP、Windows named pipe 三种连接方式
- 完整的 workspace / session / message 生命周期 API
- SSE 事件流订阅（消息、权限请求、问题、agent 事件、配置变更等）
- 服务端配置读写、模型选择、Provider API key（字符串或 OAuth token）
- 权限授予、技能读取、MCP / LSP 状态管理等
- 与服务器版本解耦：未知 JSON 字段自动忽略，向前兼容 Crush 升级

## 快速开始

```go
package main

import (
	"context"
	"fmt"

	"github.com/roberChen/crush-sdk"
)

func main() {
	// DefaultClient 连接默认的 per-user socket
	// ($XDG_RUNTIME_DIR 或 $TMPDIR 下的 crush-<uid>.sock)。
	c, err := client.DefaultClient("/path/to/project")
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	if err := c.Health(ctx); err != nil {
		panic(err)
	}

	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		panic(err)
	}
	for _, ws := range workspaces {
		fmt.Println(ws.ID, ws.Path)
	}
}
```

显式指定地址用 `client.NewClient(path, network, address)`，`network` 为 `unix`、`tcp` 或 `npipe`：

```go
c, err := client.NewClient("/path/to/project", "tcp", "127.0.0.1:8080")
```

完整示例见 [examples/e2e](examples/e2e)。

## 包结构

| 包 | 内容 |
| --- | --- |
| 根包（`client`） | `Client` 类型与全部 RPC 方法 |
| `proto` | 线上类型：消息、workspace、session、事件、权限、技能、工具 |
| `config` | 与 Crush 配置类型 wire 兼容的镜像 |
| `oauth` | OAuth2 token 类型 |
| `pubsub` | 事件流使用的信封与 payload 类型 |
| `message` | 消息附件类型 |
| `tools` | 工具名与权限参数结构体 |
| `lsp` | LSP server 状态常量 |
| `csync` | `config.Config` 使用的并发安全 map |

## 与上游的关系

代码提取自 [charmbracelet/crush](https://github.com/charmbracelet/crush) 的
`internal/client`（及其依赖的 `internal/proto` 等）。原始 `config` 包深度耦合
db/session/shell 等后端实现，这里保留为 wire 兼容的精简镜像；`proto`、`pubsub`、
`oauth`、`csync` 则基本原样保留，方便后续跟随上游同步更新。

要求 Go 1.27+。

## License

[FSL-1.1-MIT](LICENSE.md) © Charmbracelet, Inc.（上游代码版权）
