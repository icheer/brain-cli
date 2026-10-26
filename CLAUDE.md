# CLAUDE.md

## 项目是什么

`brain`：给宿主 Agent 用的大模型网关外脑 CLI（Go）。通过 OpenAI Chat Completions 兼容协议（`/v1/chat/completions` + `/v1/models`）调用网关，定位是纯咨询/分析工具，**不做** Function Calling、不做多轮会话状态。

## 开发上下文入口

- **先读 [DESIGN.md](DESIGN.md) 第 0 节「已定决策」**——两轮设计评审敲定的结论，不要重新讨论已定项
- 当前状态：**v0.8.0 已发布（M0–M8）**——`--extra-body`/`--extra-headers` 通用透传口、`brain compare` 并行交叉验证（别名=模型组快捷方式、`--blind` 匿名）、CLI 全量双语 zh/en（`BRAIN_LANG`/locale/UTC+8 检测链；README 英文主版 + README.zh-CN.md 互链）；CI e2e 因网关 IP 风控已降级 continue-on-error；本地 `.env` 存放网关 base_url/api_key 供真实联调，已加入 .gitignore，**永不提交**
- 架构、配置文件格式、退出码表、超时重试策略等规范全部在 DESIGN.md 内，以它为准

## 铁律

- **任何密钥不得进入仓库**：config.yaml 含 api_key 与别名（aliases 段），已在 .gitignore 中，整体 0600、永不提交
- stdout 只输出模型正文；警告/心跳/耗时走 stderr（详见 DESIGN.md 3.2）
- 修改行为规范前，先确认没有违背 DESIGN.md 第 0 节的已定决策；若要推翻某条决策，先更新第 0 节表格

## 语言与工程约定

- Go，`CGO_ENABLED=0` 静态编译；目标平台：darwin/linux/windows × amd64/arm64
- 配置路径用 `os.UserConfigDir()` 解析，不要硬编码 Linux 路径
- 文档与代码注释使用中文；commit message 可中英混用，遵循常规动词开头
