# brain-cli

> 仓库名 `brain-cli`，二进制命令名为 **`brain`**。

`brain` 是一个给宿主 Agent（Claude Code、Gemini CLI 等）当"外脑"的极简命令行工具：通过 OpenAI Chat Completions 兼容协议调用大模型网关，负责一次高质量的咨询/分析调用，把结果干净地交还给 Agent。

## 核心特性

- **Agent-Friendly**：stdout 只输出模型正文，元数据/警告/心跳全走 stderr；管道 / `-f` 多文件 / `-i` 图片多模态输入开箱即用
- **模型别名路由**：`fast` / `reason` / `coding` / `vision` 别名 + 自动故障转移（重试退避、换链上模型、stderr 提示），Agent 无需记忆模型名
- **可解析**：退出码 0-7 / 130 全量语义；`--json` 成败恒定输出单对象（`jq` 无条件解析，`error.code` 与退出码一致）
- **健壮传输**：始终 SSE + 15s 心跳 + 总预算控制（重试也吃同一个 deadline）
- **无状态**：多轮上下文由调用方拼接
- **跨平台**：darwin / linux / windows × amd64 / arm64，goreleaser 发布，静态编译约 9MB

## 30 秒开始

```bash
# 安装（三选一）
bash skills/brain/install.sh        # 技能场景：从 Release 下载二进制到技能目录（SHA256 校验）
# 或从 https://github.com/icheer/brain-cli/releases 下载对应平台包
go install github.com/icheer/brain-cli@latest   # 开发者

# 配置（一次性；密钥只落本机 0600 文件，绝不进仓库）
brain config set base_url https://your-gateway/v1
brain config set api_key sk-xxxx
brain config set default_model fast   # 可选；别名映射参考 models.example.yaml

# 第一问
brain ping
brain ask -m fast "用一句话介绍你自己"
```

给 Agent 用：把 `skills/brain/` 复制或链接进 `.claude/skills/`（或 `.agents/skills/`），SKILL.md 会引导宿主 Agent 何时、如何、何时不调用 brain。

## 状态与路线

**:construction: M1 完成，M2 进行中**——里程碑与验收状态见 [TODO.md](TODO.md) 第 7 节。

| 里程碑 | 内容 | 状态 |
| --- | --- | --- |
| M0 (MVP) | 配置体系 + `ask`（参数/stdin/`-f`/`-i`/`-s`）+ 别名解析 + 退出码 + `brain models` | ✅ 完成 |
| M1 | SSE 传输 + 心跳 + fallback/重试 + `--json` + `brain ping` + 分发流水线 | ✅ 完成 |
| M2 | `--stream` / `--show-thinking` / `brain init` 交互引导 / Homebrew | 🔜 进行中 |
