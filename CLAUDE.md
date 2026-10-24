# CLAUDE.md

## 项目是什么

`brain`：给宿主 Agent 用的大模型网关外脑 CLI（Go）。通过 OpenAI Chat Completions 兼容协议（`/v1/chat/completions` + `/v1/models`）调用网关，定位是纯咨询/分析工具，**不做** Function Calling、不做多轮会话状态。

## 开发上下文入口

- **先读 [TODO.md](TODO.md) 第 0 节「已定决策」**——两轮设计评审敲定的结论，不要重新讨论已定项
- 当前状态：设计定稿（v2），**M0 尚未开始**，从 TODO.md 第 7 节里程碑的 M0 起步
- 架构、配置文件格式、退出码表、超时重试策略等规范全部在 TODO.md 内，以它为准

## 铁律

- **任何密钥不得进入仓库**：`config.yaml` 含 `api_key`，已在 .gitignore 中，永不提交；`models.yaml` 可提交（不含密钥）
- stdout 只输出模型正文；警告/心跳/耗时走 stderr（详见 TODO.md 3.2）
- 修改行为规范前，先确认没有违背 TODO.md 第 0 节的已定决策；若要推翻某条决策，先更新第 0 节表格

## 语言与工程约定

- Go，`CGO_ENABLED=0` 静态编译；目标平台：darwin/linux/windows × amd64/arm64
- 配置路径用 `os.UserConfigDir()` 解析，不要硬编码 Linux 路径
- 文档与代码注释使用中文；commit message 可中英混用，遵循常规动词开头
