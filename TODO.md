# brain — 大模型网关外脑 CLI 设计与研发 TODO（v2）

> v2 修订要点：API Key 不再编译进二进制（公开分发前提）；新增完整配置体系章节；
> 明确跨平台分发方案（goreleaser + GitHub Actions）；新增退出码 / 输入上限 / 超时重试规范；
> 流式策略改为「传输层 SSE + stdout 一次性输出」；模型清单以 `/v1/models` 动态拉取为主 + 本地缓存；
> 明确非目标；新增里程碑划分。

## 0. 已定决策

| 决策点 | 结论 |
| --- | --- |
| 语言 | Golang（交叉编译成熟、单二进制、goreleaser 生态完善） |
| 协议 | OpenAI Chat Completions 兼容（`/v1/chat/completions` + `/v1/models`） |
| 分发 | 公开 GitHub 仓库（源码 + 多平台二进制 Release）；darwin / linux / windows × amd64 / arm64 |
| 密钥 | **不硬编码、不进仓库**。走配置文件 / 环境变量；`-ldflags` 仅注入版本号与 commit |
| 模型清单 | 以网关 `/v1/models` 动态拉取为准 + 本地缓存；本地 `models.yaml` 只负责别名与场景标注 |
| 会话状态 | v1 无状态，由调用方自行拼接上下文；`--session` 暂缓 |
| 文件类型 | 文本文件 + 图片（本地路径 / URL）；PDF、音频明确排除，后续看情况 |
| 流式 | 传输层始终 SSE（防代理/网关掐断空闲连接、支撑长推理）；stdout 默认等完成一次性输出；`--stream` 增量打印供人肉交互（M2） |
| 定位 | 纯咨询/分析外脑，不做 Function Calling |

## 1. 目标与非目标

**目标**
- 给宿主 Agent 用的"外脑"：低摩擦、可脚本化、输出可解析、行为可预期。
- 开箱即用：一次 `brain config set`（或 `brain init`）后立即可用。

**非目标（写入 README，防止范围蔓延）**
- 不做 Function Calling，不替宿主 Agent 执行工具。
- 不做多轮会话管理（无状态工具，调用方拼上下文）。
- 不做 PDF / 音视频解析（v1）。

## 2. 配置体系（公开分发后的核心）

### 2.1 文件布局：个人配置与可共享清单分离
- `config.yaml` — 个人配置，**含密钥**，权限 0600。
- `models.yaml` — 可共享的别名/场景标注，**不含密钥**，可提交进仓库供团队复用。

跨平台路径（用 `os.UserConfigDir()` 解析）：
- Linux: `~/.config/brain/`
- macOS: `~/Library/Application Support/brain/`
- Windows: `%AppData%\brain\`
- `BRAIN_CONFIG` / `BRAIN_MODELS` 环境变量可指定完整文件路径覆盖。

配置覆盖优先级：`CLI Flags > 环境变量（BRAIN_API_KEY / BRAIN_BASE_URL / BRAIN_TIMEOUT）> config.yaml > 代码内默认值`。
（base_url 的默认值可编译注入——URL 非机密；密钥绝不注入。）

### 2.2 config.yaml 示例
```yaml
base_url: https://gw.example.com/v1
api_key: sk-xxxx
default_model: fast          # 对应 models.yaml 中的别名
timeout_seconds: 300          # 兼容长推理模型
max_input_bytes: 1048576      # 输入保护上限（1 MiB）
retry_max: 2                  # 自动重试次数上限
models_cache_ttl_seconds: 86400
```

### 2.3 models.yaml 示例（别名 → 真实模型 + fallback 链 + 场景标注）
```yaml
aliases:
  fast:
    models: [gemini-2.5-flash, gpt-4.1-mini]   # 前者为主，故障自动切换后者
    use: "快速问答、简单总结"
  reason:
    models: [o3, deepseek-r1]
    use: "复杂推理、方案交叉验证"
  coding:
    models: [claude-sonnet, gpt-4.1]
    use: "代码审查、重构建议"
  vision:
    models: [gemini-2.5-pro]
    use: "图表/截图解析"
```

### 2.4 配置管理命令与密钥安全
- [ ] `brain config path | get <key> | set <key> <value> | list | edit`
- [ ] `brain init`：交互式引导写入 config.yaml（任何命令检测到无配置时，stderr 给出确切的下一条命令提示）
- [ ] 密钥读取顺序：环境变量 > config.yaml；文件权限非 0600 时 stderr 警告
- [ ] 不提供 `--api-key` flag（避免进入 shell history / `ps` 输出；一次性使用走环境变量）

## 3. CLI 交互规范

### 3.1 输入
- [ ] 位置参数直接传 prompt：`brain ask -m coding "重构此函数"`
- [ ] Stdin 管道：`cat log.txt | brain ask -m reason "分析日志"`
- [ ] `-f <file>` 文本文件，**可多次**（多文件比对），拼接时带分隔头：
  ```
  ----- file: src/app.go -----
  <内容>
  ```
- [ ] `-i <path|url>` 图片，**可多次**（多图比对）；本地文件自动 sniff MIME 转 `data:image/...;base64,...`，URL 直接透传
- [ ] `-s / --system <text>` 系统提示词（Agent 高频需求：约束外脑角色与输出格式）
- [ ] 多来源拼接顺序：位置参数 → `-f`（按出现顺序）→ stdin（若有）
- [ ] 输入上限保护：默认 1 MiB，超限**报错退出**（exit 6）并在 stderr 提示 `--max-input-bytes`；不静默截断，保证 Agent 可感知

### 3.2 输出
- [ ] **stdout**：只输出模型正文文本；默认等传输完成后一次性打印
- [ ] **stderr**：警告、耗时、心跳（`[waiting for model… 45s | 1.2k tokens]`）、fallback 切换提示
- [ ] `--json`：结构化输出（content、thinking、实际使用模型、usage、latency_ms、finish_reason、error）
- [ ] `--show-thinking`：推理模型思维链走 **stderr**，stdout 仍只有正文；`--json` 中两个字段并存
- [ ] `--stream`（M2）：token 增量打印到 stdout，供人类交互使用

### 3.3 退出码规范（Agent 消费的第一优先级信号）
| code | 含义 |
| --- | --- |
| 0 | 成功 |
| 1 | 用法错误（参数/标志非法） |
| 2 | 认证失败（401/403，提示检查 api_key） |
| 3 | 限流且重试耗尽（429） |
| 4 | 超时 |
| 5 | 上游/网络错误（含重试耗尽的 5xx） |
| 6 | 输入超限 |
| 7 | 配置缺失（无 key / 无 base_url，stderr 给出修复命令） |

### 3.4 超时与重试
- [ ] 默认 `timeout_seconds: 300`（长推理模型 3-5 分钟属正常），`--timeout` 可调
- [ ] 重试条件：连接失败 / 429 / 5xx；指数退避 + 遵循 `Retry-After`；上限 `retry_max`（默认 2），且受总 deadline 约束
- [ ] SKILL.md 写明：宿主 Agent 的 Bash 工具超时须大于 brain 超时（如 360s），否则进程会被宿主先杀掉

## 4. 模型解析与故障转移
- [ ] `-m` 解析顺序：models.yaml 别名（取 fallback 链首位）→ 视为字面模型 ID → 报错并列出相近可用模型
- [ ] `brain models`：合并展示本地别名（含场景标注）+ `/v1/models` 实际可用列表；远端结果缓存于用户缓存目录（TTL 默认 24h，`--refresh` 强制拉取；离线降级用缓存并 stderr 提示）
- [ ] Fallback 规则：仅在**尚未输出任何正文内容**前允许切换（避免半截输出污染 stdout）；切换必须 stderr 提示；`--json` 记录最终使用的模型
- [ ] `brain ping`：自检连通性 / 鉴权 / 默认模型可用性（SKILL.md 引导 Agent 第一步先跑它）

## 5. 分发与工程化
- [ ] goreleaser 构建矩阵：darwin/amd64+arm64、linux/amd64+arm64、windows/amd64+arm64；`CGO_ENABLED=0` 静态编译；产出 SHA256SUMS
- [ ] GitHub Actions：打 tag 触发 release；CI 跑 `go test` + 跨平台构建冒烟 + 一次真实网关调用的 e2e（key 走 repo secret）
- [ ] `--version`：ldflags 注入 git tag / commit
- [ ] README：30 秒快速开始（安装 + 两条命令配置 + 第一次调用）+ 一键安装脚本
- [ ] 可选：Homebrew tap；`go install` 作为开发者备选
- [ ] SKILL.md 随仓库分发（`skills/brain/SKILL.md`），README 说明如何链接/复制进 `.claude/skills/` 或 `.agents/skills/`

## 6. 配套 Agent Skill（SKILL.md）设计
- [ ] **触发时机**：复杂推理交叉验证、第二意见代码审查、视觉/图表解析、死循环时的破局咨询
- [ ] **不触发时机**（防滥用与费用控制）：Agent 自己能完成的事、简单事实查询、连续高频调用（写明节流预期）
- [ ] 命令模板：管道日志分析、多文件审查（`-f` 多次）、多图比对（`-i` 多次）、`-s` 角色约束
- [ ] 工程注意事项：宿主超时设置、按退出码决定重试还是换模型、用 `--json` 核对 usage
- [ ] 验证指引：外脑输出后的自检与落地流程

## 7. 里程碑
- **M0（MVP）**：配置体系（2.1–2.4）+ `ask`（参数/stdin/`-f`/`-i`/`-s`）+ 别名解析 + 退出码 + 纯净输出 + `brain models`（含缓存）
  - 验收：在真实 Agent 会话中完成「管道日志分析」与「多图比对」端到端演练
- **M1**：传输层 SSE + stderr 心跳 + fallback + `--json` + 超时重试策略 + `brain ping`
- **M2**：`--stream`、`--show-thinking`、`brain init` 交互引导、Homebrew tap
- **并行工作流**：分发流水线（第 5 节）在 M1 期间就绪，M1 结束即可公开发布

## 8. 开放问题
1. **网关密钥发放**：若网关目前只有一把共享 key，公开分发前需先支持按用户发 key（网关侧配套工作，需确认可行性与时间点）
2. **base_url 默认值**：是否适合写进公开仓库（若网关为内部部署，URL 本身是否敏感）
3. Fallback 触发范围精确化：仅 429/5xx，网络错误算不算？
4. Windows 真机测试环境来源
