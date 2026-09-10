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

## imwrap：IM 软件接入层（框架）

`imwrap` 在 `Client` 之上封装成面向聊天软件的**框架**：宿主程序只需要提供
`IMAdapter`（发送文字、发送文件，可选实现拉取历史）与自定义命令，其余全部
由框架处理（会话/模型/权限/报告/提问/过滤/日志/配置）。

支持两种对话场景，并能准确区分用户输入与 bot 自己的输出：

- **双账号**（用户与 bot 各自一个账号）：配置 `SelfAccount`，来自 bot 账号
  的消息直接忽略
- **共用账号**（bot 以同一账号轮询历史）：三层过滤，按可靠性依次为
  ① 消息 ID（`IMMessage.ID` + `MarkSelfMessage`）；② 内容回声（框架发出的
  每条文字都会被记录，回灌时按 chat + 时间窗 + `SentAt` 识别）；
  ③ `IMMessage.FromSelf` 显式声明

其他框架能力：

- 消息解析：`/` 前缀识别为命令，其余文本作为 agent prompt 触发回答；
  触发后立刻回复 ack
- 权限自动放行（YOLO）：workspace 以 YOLO 创建，SSE 权限请求自动 grant
- 可选自动拉起 `crush server`（`StartServer`，默认关闭，行为同 `crush client`），
  `Stop()` 时回收子进程
- 每轮回答完成后渲染自带样式的 HTML 文件发送：thinking、正文（完整
  markdown：标题/表格/列表/引用/分割线/删除线/图片链接/自动链接）、
  toolcall 与其 result（按 tool_call_id 配对在同一块内）；
  `edit`/`write`/`multiedit` 渲染彩色 diff；`task`/`agent` 渲染输入、
  子会话完整过程与输出，且不切换当前会话绑定
- agent 调用 question tool 时：先发送当前进度的 HTML 文件，再发文字提问；
  下一条非命令消息作为答案解析提交（序号/选项文本/yes-no/自由文本）
- 模型管理：`/models`、`/model`；一次性对话 `/ask [-m 模型] [-d 目录]
  [-t 标题]`（可选临时模型覆盖，结束恢复并公布 session id）；
  `/say <会话ID> 提示词` 向指定会话单发（均不影响当前绑定）
- 会话管理：`/sessions [关键词]`（跨目录聚合为带搜索框的 HTML 文件，避免 IM
  截断；关键词服务端预过滤）、`/switch`、`/new [-d 目录] [标题]`、
  `/info`（目录/技能/工具/上下文水位）、`/export`（HTML 导出）、
  `/summarize`（手动压缩会话）、`/status`、`/cancel`、`/help`
- 每份 HTML 报告末尾附带会话状态栏（类似 crush 本体 sidebar）：会话标题、
  目录、模型、上下文用量（进度条）、统计、git 分支与未提交数
- `/git` 命令族：无参=导出当前工作空间 git 状态（分支、变更文件、完整 diff）
  为 HTML，附带会话状态栏；`/git log [n]` 最近 n 个提交（含各自 diff 与跨提交
  合并视图）；`/git push [remote [分支]]`、`/git pull`、`/git checkout <分支>`
  （无参列出本地分支）
- 多 client 会话接管：会话被其他客户端（如 TUI）驱动时，`/status` 可见
  session busy、bot 新 prompt 自动排队；该轮回合结束（即使非 bot 触发）同样
  推送 HTML 报告并续跑排队消息
- `/export` 导出的 HTML 末尾附带会话状态栏
- workspace 目录合法性校验（存在且为目录），`/new -d`、`/ask -d` 拼写错误
  立即报错
- HTML 报告文件由框架管理生命周期：适配器实现 `FilePathSender` 时，框架将文件
  写入专用临时目录（`html_dir` 可配置，默认系统临时目录下的 imwrap-html，
  绝不落在程序运行目录），发送后立即删除；实现 `ContentFileSender` 的适配器
  则直接接收字节。`IMAdapter` 只强制 `SendText`
- diff 渲染支持**合并/并排双视图**（CSS checkbox 切换，无需 JS）：行号、
  可见的 +/- 统计、行内字符级高亮、多文件分组与 @@ hunk 样式化
- 统一日志：`imwrap.SetLogger/SetLogLevel/SetLogFile`，宿主直接用包级
  `imwrap.Debug/Info/Warn/Error` 记录日志，整个程序（含子进程输出）统一走
  同一日志管道
- JSON 配置文件：`imwrap.LoadConfig(paths...)` + `FileConfig.Apply`，支持
  `log_file`（日志落盘）与 `extra`（宿主自定义配置节），可接管整个程序的
  配置加载
- 自动 summary 与 crush 本体一致：由服务端按上下文水位自动触发，SDK 会话
  天然生效；`/summarize` 手动触发，`disable_auto_summarize` 配置可关闭
- `RegisterCommand` 注册自定义命令；每个 IM 会话绑定一个 Crush session，
  忙碌时新 prompt 自动排队

```go
cfg := imwrap.Config{Client: c, Adapter: myAdapter, SelfAccount: "bot@im"}
if fileCfg, err := imwrap.LoadConfig("imwrap.json"); err == nil {
    fileCfg.Apply(&cfg)
}
w, _ := imwrap.New(cfg)
_ = w.Start(ctx)          // StartServer=true 时自动拉起 crush server
defer w.Stop()

// IM 收到消息时（框架自行过滤 bot 自身输出）：
_ = w.HandleMessage(ctx, imwrap.IMMessage{ChatID: chat, ID: id, Sender: sender, Text: text})

// 编程接口（同样被内置命令使用）：
_ = w.AskOnce(ctx, chat, "快速看下这个问题", imwrap.AskOptions{Model: "openai/gpt-5"})
_ = w.SummarizeSession(ctx, chat)
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
