# CLAUDE.md

## 项目是什么

`brain`：给宿主 Agent 用的大模型网关外脑 CLI（Go）。通过 OpenAI Chat Completions 兼容协议（`/v1/chat/completions` + `/v1/models`）及专用协议（TypeSafe `/v1/systemone`、Gemini/OpenAI Images）调用外部模型，定位是纯咨询/分类/生图工具，**不做** Function Calling、不做多轮会话状态。

## 开发上下文入口

- **先读 [DESIGN.md](DESIGN.md) 第 0 节「已定决策」**——评审敲定的架构红线，不要重开已定项；当前版本 **v0.13.2 已发布（M0–M13）**，完整行为规范（§1–6）、里程碑演进记录（§7）与开放问题（§8）全部以 [DESIGN.md](DESIGN.md) 为唯一权威台账。
- **发版回归看 [CUJ.md](CUJ.md)**——关键用户旅程验收表与历史真实事故登记（新增行为类变更须先补/改对应行）。
- **命令矩阵速览**（按命令树注册序展示，全量中英双语 `BRAIN_LANG`/locale/UTC+8）：
  - **五大原语**：`ask`（单次咨询 + 别名链故障转移 + SSE/心跳 + XML 多源区隔） · `compare`（同题并发交叉验证 + `--blind`） · `council`（首轮并行盲评 + 蛇形/轮转/固定串行辩论 + `--blind` + 透镜=上下文） · `decide`（System One 类型化判断 `/v1/systemone`，保键序） · `draw`（Gemini 原生 / OpenAI Images 双协议生图，自动落盘，不自动重试）
  - **配置与运维**：`init`（交互向导，覆盖重建保留既有别名/draw/decide 段） · `config`（`path|get|set|list|edit`） · `models`（按 `base_url` 隔离缓存） · `ping`（连通/鉴权/逐链实弹探测） · `update`（三层发现技能目录 + 委托 `install.sh` + `.old` 回滚保护）
  - **通用契约**：退出码 `0-7 / 130`；`--json` 成败恒定单对象；`--extra-body` / `--extra-headers` 通用逃生口（保留字保护，快车道 flag 优先）；`--dry-run` 零网络核对请求形状。
- 本地 `.env` 存放网关凭据供真实联调（CI e2e 因网关 IP 风控设 `continue-on-error`），已加入 `.gitignore`，**永不提交**。

## 铁律

- **任何密钥不得进入仓库**：`config.yaml` 含 `api_key` 与 `aliases` 段，已在 `.gitignore` 中，整体 `0600`、永不提交
- stdout 只输出模型正文（`draw` 例外为单行保存路径，`decide` 为单行 answers JSON）；警告/心跳/耗时一律走 stderr（详见 [DESIGN.md](DESIGN.md) 3.2）
- 修改行为规范前，先确认没有违背 [DESIGN.md](DESIGN.md) 第 0 节的已定决策；若要推翻某条决策，先更新第 0 节表格

## 语言与工程约定

- Go，`CGO_ENABLED=0` 静态编译；目标平台：`darwin/linux/windows × amd64/arm64`
- 配置路径用 `os.UserConfigDir()` 解析，不要硬编码 Linux 路径
- 命令树通过 `newRootCmd()` 按需构造，禁止引入包级可变 flag/options 单例（防止单测跨调用状态污染与 i18n init 期冻结）
- 文档与代码注释使用中文；commit message 可中英混用，遵循常规动词开头
