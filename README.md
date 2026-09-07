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

## imwrap：IM 软件接入层

`imwrap` 包在 `Client` 之上封装了一个面向聊天软件（企业微信、Telegram、
Slack 等支持 CLI 操作的 IM）的控制器。宿主程序实现 `IMAdapter`
（发送文字、发送文件，可选实现拉取历史），把收到的每条 IM 消息喂给
`Wrapper.HandleMessage` 即可：

- 消息解析：`/` 前缀识别为命令，其余文本作为 agent prompt 触发回答
- 权限自动放行（YOLO）：workspace 以 YOLO 创建，SSE 权限请求自动 grant
- 触发对话后立刻回复 ack；每轮回答完成后，把本轮完整信息（thinking、
  tool call、tool result、正文、finish 原因）渲染为自带样式的 HTML 文件
  发送，并附一条文字摘要
- tool call 分类渲染：`edit`/`write`/`multiedit` 渲染为彩色 diff；
  `task`/`agent`（子 agent，本质是新会话）渲染输入、子会话完整过程
  （其 toolcall 与输出）和最终输出，且不会切换当前会话绑定
- agent 调用 question tool 时：先发送当前进度的 HTML 文件，再发一条
  文字提问；用户的下一条非命令消息会作为答案解析并提交
  （支持序号、选项文本、yes/no、自由文本）
- 模型管理：`/models` 罗列（标注当前项与上下文窗口），`/model` 查看
  或切换（支持 `provider/model` 或裸模型名）
- 一次性对话：`/ask [-m 模型] [-d 目录] [-t 标题] 提示词` 在新 session
  中执行（可选临时模型覆盖，结束后恢复），完成时公布 session id；
  `/say <会话ID> 提示词` 向指定会话发一条消息，两者都不影响当前绑定
- 会话管理：`/sessions`（跨目录罗列）、`/switch`、`/new [-d 目录] [标题]`
  （可指定工作目录，自动创建对应 workspace 与事件订阅）、
  `/info`（目录、技能、工具、上下文水位）、`/export`（导出完整会话为
  HTML）、`/status`、`/cancel`、`/help`
- `RegisterCommand` 注册自定义命令（回调函数），可回复文字与文件
- 每个 IM 会话绑定一个 Crush session；会话忙碌时新 prompt 自动排队

```go
w, _ := imwrap.New(imwrap.Config{Client: c, Adapter: myAdapter})
_ = w.Start(ctx)
defer w.Stop()

// IM 收到消息时：
_ = w.HandleMessage(ctx, imwrap.IMMessage{ChatID: chat, Text: text})

// 编程接口（同样被内置命令使用）：
_ = w.AskOnce(ctx, chat, "快速看下这个问题", imwrap.AskOptions{Model: "openai/gpt-5"})
models, _ := w.ListModels(ctx, chat)
info, _ := w.GetSessionInfo(ctx, chat) // 目录/技能/工具/上下文水位
```

宿主程序骨架见 [examples/imhost](examples/imhost)。

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
| `imwrap` | 面向 IM 软件的 wrapper（见下文） |

## 与上游的关系

代码提取自 [charmbracelet/crush](https://github.com/charmbracelet/crush) 的
`internal/client`（及其依赖的 `internal/proto` 等）。原始 `config` 包深度耦合
db/session/shell 等后端实现，这里保留为 wire 兼容的精简镜像；`proto`、`pubsub`、
`oauth`、`csync` 则基本原样保留，方便后续跟随上游同步更新。

要求 Go 1.27+。

## 版本

本模块遵循语义化版本，以 git tag 发布（`go get github.com/roberChen/crush-sdk@v0.1.0`）。
当前版本：**v0.1.0**（首个发布：SDK 客户端 + imwrap IM 接入层）。

## License

[FSL-1.1-MIT](LICENSE.md) © Charmbracelet, Inc.（上游代码版权）
