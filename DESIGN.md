# brain — 大模型网关外脑 CLI 设计文档

> 本文档是 brain 的设计与决策台账：第 0 节「已定决策」为评审拍板结论，不重开讨论；
> 第 1–6 节为行为规范（架构以本档为准）；第 7 节里程碑进度、第 8 节开放问题。
> 2026-10-25 由 TODO.md 更名——M0–M3 全部落地后，它早已不是待办清单。

## 0. 已定决策

| 决策点 | 结论 |
| --- | --- |
| 语言 | Golang（交叉编译成熟、单二进制、goreleaser 生态完善） |
| 协议 | OpenAI Chat Completions 兼容（`/v1/chat/completions` + `/v1/models`） |
| 分发 | 公开 GitHub 仓库（源码 + 多平台二进制 Release）；darwin / linux / windows × amd64 / arm64 |
| 密钥 | **不硬编码、不进仓库**。走配置文件 / 环境变量；`-ldflags` 仅注入版本号与 commit |
| 模型清单 | 以网关 `/v1/models` 动态拉取为准 + 本地缓存；别名与场景标注在 config.yaml 的 aliases 段 |
| 配置文件布局 | **单文件 config.yaml**（2026-10-26 由 config.yaml+models.yaml 合并，breaking）：含密钥与 aliases 段，整体 0600、永不提交——拆分买的是「可提交共享别名」，从未兑现而摩擦每天在付；共享走 config.example.yaml 复制粘贴，将来有真实团队需求可加回可选覆盖文件（增量不破坏）；`BRAIN_MODELS` 已删除 |
| 会话状态 | v1 无状态，由调用方自行拼接上下文；`--session` 暂缓 |
| 文件类型 | 文本文件 + 图片（本地路径 / URL）；PDF、音频明确排除，后续看情况 |
| 流式 | 传输层始终 SSE（长连接可承载网关侧 keep-alive，防中间层掐断空闲连接）；**stderr 心跳是纯本地进度显示，不产生网络流量**；stdout 默认等完成一次性输出；`--stream` 增量打印供人肉交互（M2） |
| 定位 | 纯咨询/分析外脑，不做 Function Calling |
| 输出落盘 | 不做 `-o`；统一用 shell 重定向 `> file`（SKILL.md 写明该模式）；Windows 重定向编码出现真实用户反馈再考虑 |
| `--json` 失败语义 | 无论成败，stdout 恒定输出**一个** JSON 对象（失败时填 `error` 字段），退出码照旧（M1 落地） |
| 采样参数 | v1 不暴露也不默认传 `temperature` 等采样调优参数；**max_tokens 例外**（2026-10-25 第二轮双外脑评审收敛后修订）——`--max-output-tokens` 作为输出预算控制 opt-in 透传：防思维链吃满输出预算导致空正文、限制长输出费用；0=字段缺席 |
| aliases 段缺失 | 可选；缺失时 `-m` 直接按字面模型 ID 处理 |
| fallback 范围 | 连接失败 / 超时 / 429 / 5xx / 404 可切换；401/403 不切（直接 exit 2）；已输出正文永不切 |
| base_url 默认值 | 公开 release 不带默认值；缺失 → exit 7 引导 `brain config set`；内部定制走 ldflags 注入（同版本号机制） |
| 换行约定 | stdout 正文末尾保证恰好一个 `\n`；`--json` 单行 JSON + `\n`（重定向产物即干净文件） |
| 工程骨架 | Go module `github.com/icheer/brain-cli`；CLI 框架 cobra；YAML 用 yaml.v3；第三方依赖仅此二者 |
| 技能分发体积 | 技能目录不内置二进制（Go+TLS 实测下限 ~6.4MB，≤1MiB 不可达）；携带 install.sh 从 Release 按需下载 + SHA256 校验；Release 统一 `-s -w` 剥离 + 全平台 tar.gz |
| 流内错误事件 | 真实网关（one-api 系）会以 HTTP 200 + `data:{"error":{code}}` 回错；按其 code 转成 StatusError 参与分类（404/model_not_found → 立即换模型） |
| 空正文语义 | 正文为空仍 exit 0 + stderr 警告（含 finish_reason 与思维链长度提示）；思维链只在 `--json` 的 thinking 字段提供 |
| reasoning_effort | opt-in 纯透传：`-e/--reasoning-effort` 设置才携带该字段；不设时字段缺席、各模型用自身默认；不校验枚举；不做厂商方言翻译（网关职责——实测网关已译 Anthropic 方言） |
| `--json` 错误结构 | error 对象扩展 `type`（auth/model_not_found/rate_limited/timeout/upstream/network/invalid_request/config_missing/input_too_large/usage/interrupted）、`retryable`、`upstream_status`、`attempted_models`；纯增量，code/message 语义不变 |

## 1. 目标与非目标

**目标**
- 给宿主 Agent 用的"外脑"：低摩擦、可脚本化、输出可解析、行为可预期。
- 开箱即用：一次 `brain config set`（或 `brain init`）后立即可用。
- 与宿主自带 subagent 机制**互补而非替代**：subagent = 同模型+工具+独立上下文（适合仓库级探索）；brain = 跨厂商模型+纯文本进出（适合独立第二意见，同源自我复核检不出系统性偏差）。对比表见 README「brain 与宿主 subagent 的区别」。

**非目标（写入 README，防止范围蔓延）**
- 不做 Function Calling，不替宿主 Agent 执行工具。
- 不做多轮会话管理（无状态工具，调用方拼上下文）。
- 不做 PDF / 音视频解析（v1）。

## 2. 配置体系（公开分发后的核心）

### 2.1 文件布局：单文件（2026-10-26 合并）
- `config.yaml` — 个人配置 + 别名映射（aliases 段），**含密钥**，整体权限 0600、永不进仓库。
  （原 models.yaml 并入；合并理由与可逆路径见第 0 节「配置文件布局」行）

跨平台路径（用 `os.UserConfigDir()` 解析）：
- Linux: `~/.config/brain/`
- macOS: `~/Library/Application Support/brain/`
- Windows: `%AppData%\brain\`
- `BRAIN_CONFIG` 环境变量可指定完整文件路径覆盖。

配置覆盖优先级：`CLI Flags > 环境变量（BRAIN_API_KEY / BRAIN_BASE_URL / BRAIN_TIMEOUT / BRAIN_DEFAULT_MODEL / BRAIN_USER_AGENT）> config.yaml > 代码内默认值`。
（base_url 的默认值可编译注入——URL 非机密；密钥绝不注入；BRAIN_DEFAULT_MODEL 使 CI/容器可纯环境变量运行、零 config.yaml。）

### 2.2 config.yaml 示例
```yaml
base_url: https://gw.example.com/v1
api_key: sk-xxxx
default_model: fast          # 对应 aliases 段中的别名
timeout_seconds: 300          # 兼容长推理模型
max_input_bytes: 1048576      # 输入保护上限（1 MiB）
retry_max: 2                  # 自动重试次数上限
models_cache_ttl_seconds: 86400
user_agent: ""                # 逃生口：个别网关校验 UA 取值时覆盖默认 brain/<version>；不伪装浏览器 UA
# aliases 段见 2.3（可选）
```

### 2.3 config.yaml 的 aliases 段示例（别名 → 真实模型 + fallback 链 + 场景标注）
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
- [x] `brain config path | get <key> | set <key> <value> | list | edit`
- [x] `brain init`：交互式引导写入 config.yaml（任何命令检测到无配置时，stderr 给出确切的下一条命令提示）；可选交互生成 aliases 段（拉 /v1/models 做选单，序号或完整 ID 均可，多个组成 fallback 链）——修复最小安装下 SKILL 模板别名 404 的冷启动断点（2026-10-25 外脑评审证实该断点）；覆盖重建时原有别名默认保留
- [x] 密钥读取顺序：环境变量 > config.yaml；文件权限非 0600 时 stderr 警告
- [x] 不提供 `--api-key` flag（避免进入 shell history / `ps` 输出；一次性使用走环境变量）

## 3. CLI 交互规范

### 3.1 输入
- [x] 位置参数直接传 prompt：`brain ask -m coding "重构此函数"`
- [x] Stdin 管道：`cat log.txt | brain ask -m reason "分析日志"`
  - stdin 读取判定：仅真实管道（`|`）或重定向的常规文件（`< file`）时读取；终端与 socket（后台任务环境，写端永不关闭）跳过——否则 ReadAll 永久阻塞
- [x] `-f <file>` 文本文件，**可多次**（多文件比对），拼接时带分隔头：
  ```
  ----- file: src/app.go -----
  <内容>
  ```
- [x] `-i <path|url>` 图片，**可多次**（多图比对）；本地文件自动 sniff MIME 转 `data:image/...;base64,...`，URL 直接透传
- [x] `-s / --system <text>` 系统提示词（Agent 高频需求：约束外脑角色与输出格式）
- [x] 多来源拼接顺序：位置参数 → `-f`（按出现顺序）→ stdin（若有）
- [x] 输入上限保护：默认 1 MiB，超限**报错退出**（exit 6）并在 stderr 提示 `--max-input-bytes`；不静默截断，保证 Agent 可感知
- [x] 完全无输入（无位置参数 / 无 `-f` / stdin 无管道）→ exit 1 + 提示
- [x] `-f` 文件须为合法 UTF-8 文本（二进制/PDF → exit 1，明确报错不静默透传）；读取失败（不存在/是目录/无权限）归 exit 1；读取流式限长（防 `/dev/zero` 类特殊文件 OOM）
- [x] 图片不计入 `max_input_bytes`（只对装配后文本计量）；单图上限 20 MiB（解码后）
- [x] `-i` 图片块在 content 数组中排在文本块之后；`-s` 可重复，以空行连接
- [x] `-e / --reasoning-effort <档位>`：推理深度纯透传（第 0 节已定；不设则请求体无此字段，档位因模型而异，如智谱 GLM-5.3 仅 low/high/max）
- [x] `--max-output-tokens <N>`：输出预算（max_tokens）纯透传（第 0 节修订后的例外；0=字段缺席用网关默认，负值 exit 1）
- [x] `--dry-run`：组装完成后输出最终 messages（单行 JSON，本地图片 data URL 截断展示），不发起网络请求；与 `--json`/`--stream` 互斥

### 3.2 输出
- [x] **stdout**：只输出模型正文文本；默认等传输完成后一次性打印
- [x] **stderr**：警告、耗时、心跳（`waiting for model… 45s | ~1.2k chars`，每 15s）、fallback 切换与重试提示
- [x] `--json`：结构化输出（content、thinking、实际使用模型、requested_model、usage、latency_ms、finish_reason、error）
- [x] `--show-thinking`：推理模型思维链走 **stderr**（带标头；`--stream` 时同步增量），stdout 仍只有正文；`--json` 中两个字段并存
- [x] `--stream`：token 增量打印到 stdout，供人类交互使用；**与 `--json` 互斥**；正文一旦开始输出即禁用重试与故障转移（第 0 节铁律，中途断流保留半截正文并按原错误码退出）
- [x] 换行约定：正文末尾保证恰好一个 `\n`；`--json` 单行 JSON + `\n`
- [x] stdout 单一写点：全程序只允许一处代码写 stdout，配 golden test 逐字节守护
- [x] `--json`（M1）失败时 stdout 仍恒定输出一个 JSON 对象（`error` 字段非空，code 与退出码一致），退出码照旧，Agent 可无条件 jq 解析
- [x] 空正文处理：exit 0 + stderr 警告（finish_reason 与思维链长度提示）
- [x] `--quiet`：关闭 stderr 心跳/耗时/转移提示（警告与错误保留；供宿主把 stdout/stderr 合并进同一输出的场景）

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
| 130 | 被中断（SIGINT），区别于超时 |

### 3.4 超时与重试
- [x] 默认 `timeout_seconds: 300`（长推理模型 3-5 分钟属正常），`--timeout` 可调
- [x] 重试条件：连接失败 / 429 / 5xx；指数退避（1s/2s/4s 封顶 8s）+ 遵循 `Retry-After`；上限 `retry_max`（默认 2），且受总 deadline 约束（404/model_not_found/超时直接换模型不重试同模型）
- [x] SKILL.md 写明：宿主 Agent 的 Bash 工具超时须大于 brain 超时（如 360s），否则进程会被宿主先杀掉

## 4. 模型解析与故障转移
- [x] `-m` 解析顺序：config.yaml 别名（取 fallback 链首位）→ 视为字面模型 ID → 报错并列出相近可用模型
- [x] `brain models`：合并展示本地别名（含场景标注）+ `/v1/models` 实际可用列表；远端结果缓存于用户缓存目录（TTL 默认 24h，`--refresh` 强制拉取；离线降级用缓存并 stderr 提示）
- [x] Fallback 规则：仅在**尚未输出任何正文内容**前允许切换（一次性输出天然满足）；切换/重试均 stderr 提示；`--json` 的 model 字段记录最终使用的模型
- [x] `brain ping`：自检连通性 / 鉴权 / 默认模型可用性（SKILL.md 引导 Agent 第一步先跑它）
- [x] 无 `default_model` 且未传 `-m` → exit 7 + 修复提示
- [x] `/v1/models` 缓存放 `os.UserCacheDir()/brain/models.json`（与配置目录区分），损坏即弃用重拉
- [x] `brain ping -m <别名或ID>`：逐模型探测 fallback 链（各发一次极小补全请求；`/v1/models` 列出≠本 key 可用，探测才是硬证据）；链上至少一个可用即通过，主模型挂时 stderr 明示（防静默降级到次优模型）
- [x] `ping --json` / `models --json`：机器可读输出（ping 成败恒定单对象，error 与 ask --json 同构；`ping -m` 时含 `probes` 数组：model / ok / latency_ms / error——逐模型探测结果无需解析 stderr）

## 5. 分发与工程化
- [x] goreleaser 构建矩阵：darwin/amd64+arm64、linux/amd64+arm64、windows/amd64+arm64；`CGO_ENABLED=0` 静态编译 + `-s -w -trimpath`；产出 SHA256SUMS（配置已过 `goreleaser check`，待首个 tag 全链路验证）
- [x] GitHub Actions：打 tag 触发 release；CI 跑 `go test` + 6 目标交叉构建冒烟（真实网关 e2e 待配 repo secret 后补）
- [x] `--version`：ldflags 注入 git tag / commit
- [x] README：30 秒快速开始 + 一键安装脚本（`skills/brain/install.sh`，SHA256 校验）
- [ ] 可选：Homebrew tap（需先建 `icheer/homebrew-tap` 仓库，goreleaser 加 brew 配置即可）；`go install` 作为开发者备选
- [x] SKILL.md 随仓库分发（`skills/brain/SKILL.md`），README 说明如何链接/复制进 `.claude/skills/` 或 `.agents/skills/`

## 6. 配套 Agent Skill（SKILL.md）设计
- [x] **触发时机**：复杂推理交叉验证、第二意见代码审查、视觉/图表解析、死循环时的破局咨询
- [x] **不触发时机**（防滥用与费用控制）：Agent 自己能完成的事、简单事实查询、连续高频调用（写明节流预期）
- [x] 命令模板：管道日志分析、多文件审查（`-f` 多次）、多图比对（`-i` 多次）、`-s` 角色约束
- [x] 工程注意事项：宿主超时设置、按退出码决定重试还是换模型、用 `--json` 核对 usage
- [x] 验证指引：外脑输出后的自检与落地流程
- [x] 推理深度暗示：常见 5 档 low/medium/high/xhigh/max、智谱 GLM-5.3 仅 low/high/max 3 档；深度推演 `-e high`（更深可用 xhigh）、快问快答 `-e low`、不确定时先不设（各模型用自身默认）；`finish_reason=length` 空正文时 `-e low` 降档重试
- [x] 别名冷启动指引：别名来自本机 config.yaml 的 aliases 段（`brain init` 可生成）；未配置时 `-m fast` 会按字面模型 ID 发出而 404，用 `brain models` 查 ID 直调
- [ ] frontmatter description 的**触发命中率**在真实宿主（Claude Code + 至少一个其他宿主）实测：该触发时触发、不该触发时克制，并据此精简 description（2026-10-24 外脑评审新增：不同宿主消费 skill 的方式不同，密集描述的效果未知，这是产品成立的前提）

## 7. 里程碑
- **M0（MVP）**：配置体系（2.1–2.4）+ `ask`（参数/stdin/`-f`/`-i`/`-s`）+ 别名解析 + 退出码 + 纯净输出 + `brain models`（含缓存）
  - 状态（2026-10-24）：**功能完成**，单测全绿，真实网关 e2e 通过（别名问答 / 管道日志分析 / 多文件审查 / 图片解析 / 退出码矩阵 2/3/4/5/6/7）；待「真实 Agent 会话」验收
  - 已知网关特性：`/v1/models` 列出的模型未必对本 key 可用（401/403/404 表达无权限/不支持），别名映射需逐个实测（config.example.yaml 已注明）
  - 验收：在真实 Agent 会话中完成「管道日志分析」与「多图比对」端到端演练
  - 验收进度（2026-10-24）：真实宿主会话已完成「管道日志分析」（全局命令→管道→分析→结论落地）与「外脑项目评审」两场景；「多图比对」待日常会话补验
- **M1**：传输层 SSE + stderr 心跳 + fallback + `--json` + 超时重试策略 + `brain ping`
  - 状态（2026-10-24）：**功能完成**，单测全绿，真实网关 e2e 通过（SSE 问答 / 心跳 15s×7 / `--json` 含真实 usage+thinking / 真实故障转移 503→model_not_found 即时切换 / ping 三项自检）；分发流水线就绪（goreleaser + CI + install.sh），待首个 tag 验证
  - 真实网关经验：错误可能以 503+model_not_found 或 HTTP 200 流内 `data:{"error"}` 出现，均已按第 0 节分类处理
- **M2**：`--stream`、`--show-thinking`、`brain init` 交互引导、Homebrew tap
  - 状态（2026-10-24）：**前三项完成**（单测全绿 + 真实网关 e2e：流式增量/思维链 stderr 分流/init 向导含环境变量回落）；Homebrew 需先建 tap 仓库，暂缓
- **M3**：`-e/--reasoning-effort` 纯透传、`--json` 错误结构扩展、`--quiet`、`--dry-run`、`ping -m` 链探测 + `ping --json`、`init` 别名生成（冷启动闭环）
  - 状态（2026-10-25）：功能完成，单测全绿；来源：双外脑模型交叉评审（fox/claude-fable-5-1 + max/minimax-m3.1-flash）采信 + 用户新增的 reasoning_effort 需求；真实网关 e2e 见下
  - 评审记录：两模型独立提出 `--json` 错误粒度不足（最强共识）；minimax 发现的「SKILL 固定别名 × 最小安装 = 404 冷启动断点」经仓库核实属实并修复；拒绝 `--head`（shell 管道可解，同不做 `-o` 的哲学）；缓办 `--context-file`、`brain log`
- **M4**：`--max-output-tokens` 输出预算透传、`BRAIN_DEFAULT_MODEL` 环境变量、文档补丁（ping --json probes 字段说明）
  - 状态（2026-10-25）：完成；来源：第二轮双外脑评审（fox/claude-fable-5-1 + max/minimax-m3.1-flash，均 `-e xhigh`）交叉裁定——`--max-output-tokens` 为两模型唯一收敛项，`BRAIN_DEFAULT_MODEL` 填环境变量全量配置缺口
  - 评审记录：`--retry-on-empty`（空正文自动降档重试）与 `--max-output-tokens` 部分重叠且静默翻倍耗时，缓办；`-m m1,m2` 内联链 backlog；minimax 的 timeout/重试/SIGINT 全套与 fable 的 ping -m --json「缺口」均为**已实现功能的幻觉**（后者根源是 probes 字段未文档化，已补）——外脑无代码访问权时的过高幻觉率再次证明交叉核对不可省
- **M5**：配置单文件化（config.yaml + models.yaml 合并，breaking）
  - 状态（2026-10-26）：完成——`BRAIN_MODELS` 删除、别名进 config.yaml aliases 段（omitempty，无该段行为不变）、`init` 单文件向导且覆盖重建默认保留别名、`config path` 单行输出；决策记录见第 0 节「配置文件布局」行
  - 用户侧迁移（仅本机一户）：把旧 models.yaml 的 aliases 段并入 config.yaml 后删除前者
- **并行工作流**：分发流水线（第 5 节）在 M1 期间就绪，M1 结束即可公开发布
  - 状态：**v0.1.0 已发布**（https://github.com/icheer/brain-cli/releases/tag/v0.1.0），6 平台产物 + SHA256SUMS，install.sh 全链路（下载→校验→运行）实测通过；CI 真实网关 e2e 的 secrets 待配置

## 8. 开放问题
1. **网关密钥发放**：若网关目前只有一把共享 key，公开分发前需先支持按用户发 key（网关侧配套工作，需确认可行性与时间点）
2. Windows 真机测试环境来源

> 2026-10-24 已拍板并移入第 0 节：base_url 默认值（公开 release 不带）、fallback 触发范围、输出不做 `-o`、`--json` 失败语义、采样参数不透传、models.yaml 可选、工程骨架选型。
> 2026-10-26 已拍板并移入第 0 节：配置文件布局合并（单文件 config.yaml，原「models.yaml 可选」随之更名「aliases 段缺失」）。
