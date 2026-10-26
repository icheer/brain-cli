# brain-cli

[English](README.md) | **简体中文**

> 仓库名 `brain-cli`，二进制命令名为 **`brain`**。

`brain` 是一个给宿主 Agent（Claude Code、Gemini CLI 等）当"外脑"的极简命令行工具：通过 OpenAI Chat Completions 兼容协议调用大模型网关，负责一次高质量的咨询/分析调用，把结果干净地交还给 Agent。同一个模型复核自己的结论，检不出自己的系统性偏差——brain 把跨厂商的异构模型作为独立第二意见接入宿主。

## 核心特性

- **Agent-Friendly**：stdout 只输出模型正文，元数据/警告/心跳全走 stderr；管道 / `-f` 多文件 / `-i` 图片多模态输入开箱即用
- **模型别名路由**：`fast` / `reason` / `coding` / `vision` 别名 + 自动故障转移（重试退避、换链上模型、stderr 提示），Agent 无需记忆模型名
- **并行交叉验证**：`brain compare -m a,b -m reason` 同题并发多模型，原文按 `===== model: X =====` 分隔头拼接（`--json` 为 `responses[]`）；compare 里别名=模型组快捷方式，每腿不做故障转移；`--blind` 输出匿名 Model_A/B 防裁决的品牌偏见
- **可选推理深度与输出预算**：`-e low|medium|high|…`、`--max-output-tokens N` 纯透传（不设则各模型/网关默认；档位因模型而异，如智谱 GLM-5.3 仅 low/high/max）
- **通用透传口 `--extra-body` / `--extra-headers`**：任意请求体参数（temperature、response_format、厂商私有字段…）与 HTTP 头以 JSON 顶层 assign 透传（内联或 `@file`，可重复有序合并）；保留字拒绝防自毁，`--dry-run` 可核对合并结果；对齐 OpenAI SDK 的 extra_body/extra_headers
- **可解析**：退出码 0-7 / 130 全量语义；`--json` 成败恒定输出单对象（`jq` 无条件解析，`error.code` 与退出码一致）
- **健壮传输**：SSE 长连接（防中间层空闲断连）+ 流内错误事件识别（真实网关会以 HTTP 200 夹带 `data:{"error":…}` 回错）+ stderr 15s 心跳（本地进度显示，不产生网络流量）+ 总预算控制（重试也吃同一个 deadline）——在 one-api 系脏网关的真实行为上逐项实测，而非假设教科书式 HTTP
- **无状态**：多轮上下文由调用方拼接
- **中英双语界面**：按 `BRAIN_LANG` > `LC_ALL`/`LC_MESSAGES`/`LANG`（`zh*` → 中文，其他已设值 → 英文）> 时区兜底（UTC+8 → 中文）自动选择；随时 `BRAIN_LANG=zh|en` 强制
- **跨平台**：darwin / linux / windows × amd64 / arm64，goreleaser 发布，静态编译约 9MB

## 30 秒开始

`brain` 是 **BYOK**（Bring Your Own Key）工具：自身不提供模型服务，需要任意 **OpenAI 兼容 API** 的地址与密钥——不一定是"网关"，官方 API 直用即可，例如：

| 服务 | base_url |
| --- | --- |
| OpenAI 官方 | `https://api.openai.com/v1` |
| OpenRouter（多厂商聚合） | `https://openrouter.ai/api/v1` |
| DeepSeek | `https://api.deepseek.com/v1` |
| 智谱 | `https://open.bigmodel.cn/api/paas/v4` |

```bash
# 安装（三选一）
bash skills/brain/install.sh        # 技能场景：从 Release 下载二进制到技能目录（SHA256 校验）
# 或从 https://github.com/icheer/brain-cli/releases 下载对应平台包
go install github.com/icheer/brain-cli@latest   # 开发者

# 配置（一次性；密钥只落本机 0600 文件，绝不进仓库）
brain init                                            # 交互向导，或下面三行：
brain config set base_url https://api.openai.com/v1   # 任意 OpenAI 兼容服务
brain config set api_key sk-xxxx
brain config set default_model gpt-5.6                # 可选；完整配置示例见 config.example.yaml

# 第一问
brain ping
brain ask -m fast "用一句话介绍你自己"   # -m 也可直接用模型 ID；-e low/medium/high… 可调推理深度（可选）
```

给 Agent 用：把 `skills/brain/` 复制或链接进 `.claude/skills/`（或 `.agents/skills/`），SKILL.md 会引导宿主 Agent 何时、如何、何时不调用 brain。

## brain 与宿主 subagent 的区别

宿主 Agent 自带 subagent 机制（如 Claude Code 的 Task 工具）：同模型、带工具、独立上下文。brain 与它**互补而非替代**——最关键的一点是「换脑子」还是「再派一个自己」：

| 维度 | brain（外脑 CLI） | 宿主 subagent |
| --- | --- | --- |
| 模型 | 网关上任意外部模型（跨厂商——真·独立第二意见） | 与宿主同源（自我复核，系统性偏差与主线程相关） |
| 上下文 | 只看到你拼的 prompt（材料要自己贴/`-f` 带上） | 可自行读文件、跑命令探索仓库（适合大范围扫描） |
| 副作用 | 零——纯文本进出，无工具权限 | 继承宿主工具（读写文件、执行命令） |
| 契约 | 退出码 0-7/130、`--json`、超时/预算全部可编程 | 返回报告文本，生命周期由宿主托管 |
| 计费 | 走网关 key，`--json` 的 usage 逐次可见 | 走宿主订阅/API 账单，粒度取决于宿主 |

**各自的短板**：brain 看不到你的会话与仓库（喂料不全会答非所问），依赖网关可用性，跨厂商行为差异（推理档位、默认值）需要摸底；subagent 复核同源（同一个模型检查自己的结论，系统性偏差检不出来），并行多了 token 消耗放大。

**选型经验**：要「不同厂商模型对同一结论交叉验证 / 死循环破局」→ brain（多模型互证可一发 `brain compare`）；要「同模型并行做仓库级探索、汇总」→ subagent。两者可叠加：subagent 负责读材料，brain 负责跨模型裁决。

**「换脑子」不限于换模型——端点后面也可以是领域 agent**：信息采集、金融投研、事实核查、个人助理等细分领域，越来越多的独立 agent 以 OpenAI 兼容协议暴露端点，内部自带工具调用。对调用方而言这一切都是黑盒：传入相关上下文、拿回结果即可，远端如何推理、编排哪些工具无需关心。「不做 Function Calling」约束的只是 brain 自身（作为客户端不编排工具），从不限制端点背后是什么——正因契约只是纯文本进出，裸模型和完整 agent 都能无差别地享有同一套退出码 / `--json` / 别名 / 心跳约定。单份配置只指向一个 base_url，要混用多个端点可走聚合网关（OpenRouter / one-api 系），或用 `BRAIN_CONFIG` 指向另一份配置。

## 状态与路线

**:rocket: v0.8.0 已发布**（M0–M8）——里程碑与验收状态见 [DESIGN.md](DESIGN.md) 第 7 节。

| 里程碑 | 内容 | 状态 |
| --- | --- | --- |
| M0 (MVP) | 配置体系 + `ask`（参数/stdin/`-f`/`-i`/`-s`）+ 别名解析 + 退出码 + `brain models` | ✅ 完成 |
| M1 | SSE 传输 + 心跳 + fallback/重试 + `--json` + `brain ping` + 分发流水线 | ✅ 完成 |
| M2 | `--stream` / `--show-thinking` / `brain init` | ✅ 完成（Homebrew 待建 tap 仓库） |
| M3 | `-e` 推理深度透传 / `--json` 错误扩展（type/retryable/upstream_status/attempted_models）/ `--quiet` / `--dry-run` / `ping -m` 链探测 / `init` 别名生成 | ✅ 完成 |
| M4 | `--max-output-tokens` 输出预算透传 / `BRAIN_DEFAULT_MODEL` 环境变量 | ✅ 完成 |
| M5 | 配置单文件化：config.yaml + models.yaml 合并（breaking——别名进 aliases 段，`BRAIN_MODELS` 删除） | ✅ 完成 |
| M6 | 界面双语（zh/en）：locale / `BRAIN_LANG` / UTC+8 时区检测；README 中英双语 | ✅ 完成 |
| M7 | `brain compare` 并行独立采样（别名=模型组快捷方式、每腿无故障转移、≥1 成功即 exit 0）/ `--blind` 匿名盲测 | ✅ 完成 |
| M8 | `--extra-body` / `--extra-headers` 通用透传口（JSON 顶层 assign、保留字拒绝、`@file`、`--dry-run` 可见） | ✅ 完成 |
