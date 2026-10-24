# brain-cli

> 仓库名 `brain-cli`，二进制命令名为 **`brain`**。

`brain` 是一个给宿主 Agent（Claude Code、Gemini CLI 等）当"外脑"的极简命令行工具：通过 OpenAI Chat Completions 兼容协议调用大模型网关，负责一次高质量的咨询/分析调用，把结果干净地交还给 Agent。

## 核心特性（设计定稿）

- **Agent-Friendly**：stdout 只输出模型正文，元数据全走 stderr；管道 / 文件 / 图片多模态输入开箱即用
- **模型别名路由**：`fast` / `reason` / `coding` / `vision` 等别名 + 自动故障转移，Agent 无需记忆模型名
- **无状态**：多轮上下文由调用方拼接，工具本身不做会话管理
- **跨平台**：darwin / linux / windows × amd64 / arm64，goreleaser 发布

## 当前状态

**:construction: 设计阶段** —— 设计文档 v2 已定稿（见 [TODO.md](TODO.md)），M0 开发未开始。

## 开发路线

| 里程碑 | 内容 |
| --- | --- |
| M0 (MVP) | 配置体系 + `ask`（参数/stdin/`-f`/`-i`/`-s`）+ 别名解析 + 退出码 + `brain models` |
| M1 | SSE 传输 + 心跳 + fallback + `--json` + `brain ping` |
| M2 | `--stream` / `--show-thinking` / `brain init` / Homebrew |

详见 [TODO.md](TODO.md) 第 7 节。
