# CLAUDE.md

## 项目是什么

`brain`：给宿主 Agent 用的大模型网关外脑 CLI（Go）。通过 OpenAI Chat Completions 兼容协议（`/v1/chat/completions` + `/v1/models`）调用网关，定位是纯咨询/分析工具，**不做** Function Calling、不做多轮会话状态。

## 开发上下文入口

- **先读 [DESIGN.md](DESIGN.md) 第 0 节「已定决策」**——两轮设计评审敲定的结论，不要重新讨论已定项
- 当前状态：**v0.11.1 已发布（M0–M11；v0.11.1 修复 draw openai 路径 n 类型 bug——请求体 n 必须是 JSON 数字，字符串 "1" 会让 new-api 系网关的 uint 字段 400，golden 测试曾把错误形状锁进断言，用户附真实可用 payload 对照现形）——图像生成 `brain draw` 双协议（gemini 原生 `/v1beta/...:generateContent` + OpenAI Images `/v1/images/generations`，带 `-i` 参考图切 `/v1/images/edits` multipart；按模型名推断 + `draw.type` 覆盖）、`draw:` 配置段（model 必填不回退 default_model、base_url/api_key 兜底顶层、智能拼接 `/v1`、`/v1beta` 可写可不写）、`--aspect`/`--res` 统一语义方言翻译、响应归一四形态（b64/url/inlineData/imageUrl）、**自动落盘**（cwd→/tmp）+ stdout 恰一行路径（第 0 节「输出落盘」行的 draw 例外）、**不做自动重试**（按张计费）；draw 配置双引导：`brain config set draw.*` 与 `brain init` 向导可选步骤（覆盖重建保留既有 draw 段，2026-11-03 补——此前 init 重建会静默清空）；live 已过（gemini 原生 1K/2K/参考图编辑三发全绿）、输入装配 XML 区隔（多来源 `<user_message>`/`<attachment>`/`<stdin>` 标签、council 发言 `<speech seat round>`、system 数据声明防注入纵深）、compare/council stdout 分隔头前双空行、`brain council` 串行辩论（首轮并行盲评+蛇形串行交锋、透镜=上下文（表内须含展开后模型 ID）、模板语言随内容（汉字占比>2%→中文）、缺席退出、沉没成本保护、--order snake/rotate/fixed 发言顺序排布；v0.10.1 轮次短旗标 -C→-r，--rounds 不变）、install.sh 安装模式（`--global`/`--link`；v0.10.3 裸跑自动检测既有形态升级 + SKILL.md 随 Release 包同步，v0.10.5 安装器自身随包原子自更新，v0.10.6 `--uninstall` 反向卸载（幂等/零网络/不动配置与文档）；Windows Git Bash 原生适配：brain.exe 后缀、全局默认 ~/bin、`.gitattributes` 钉 LF）、`--extra-body`/`--extra-headers` 通用透传口（硬保留字拒绝、`-e`/`--max-output-tokens` 快车道优先）、`brain compare` 并行交叉验证（别名=模型组快捷方式、`--blind` 匿名）、CLI 全量双语 zh/en（`BRAIN_LANG`/locale/UTC+8 检测链；README 英文主版 + README.zh-CN.md 互链）；v0.10.2 移除 cobra 默认 completion 命令、命令清单按注册序展示（ask/compare/council/draw 四原语相邻前置）并双语化 help；CUJ.md 关键用户旅程验收表（发版回归底座，不随包分发）；MIT 许可；CI e2e 因网关 IP 风控已降级 continue-on-error；本地 `.env` 存放网关 base_url/api_key 供真实联调，已加入 .gitignore，**永不提交**
- 架构、配置文件格式、退出码表、超时重试策略等规范全部在 DESIGN.md 内，以它为准

## 铁律

- **任何密钥不得进入仓库**：config.yaml 含 api_key 与别名（aliases 段），已在 .gitignore 中，整体 0600、永不提交
- stdout 只输出模型正文；警告/心跳/耗时走 stderr（详见 DESIGN.md 3.2）
- 修改行为规范前，先确认没有违背 DESIGN.md 第 0 节的已定决策；若要推翻某条决策，先更新第 0 节表格

## 语言与工程约定

- Go，`CGO_ENABLED=0` 静态编译；目标平台：darwin/linux/windows × amd64/arm64
- 配置路径用 `os.UserConfigDir()` 解析，不要硬编码 Linux 路径
- 文档与代码注释使用中文；commit message 可中英混用，遵循常规动词开头
