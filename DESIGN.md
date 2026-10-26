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
| 定位 | 纯咨询/分析外脑，不做 Function Calling——指 brain 作为客户端不编排工具；端点背后是裸模型还是自带工具调用的第三方领域 agent 均不关心（OpenAI 兼容协议只约定文本进出，2026-10-26 随 README「领域 agent」段落澄清） |
| 输出落盘 | 不做 `-o`；统一用 shell 重定向 `> file`（SKILL.md 写明该模式）；Windows 重定向编码出现真实用户反馈再考虑 |
| `--json` 失败语义 | 无论成败，stdout 恒定输出**一个** JSON 对象（失败时填 `error` 字段），退出码照旧（M1 落地） |
| 采样参数 | 不为采样调优参数设专属 flag：`-e`/`--max-output-tokens` 保留为高频**快车道**（opt-in 纯透传，空/0=字段缺席）；其余请求体参数（temperature/top_k/response_format/厂商私有字段…）统一走 `--extra-body`（M8，2026-10-26 修订原「v1 不暴露也不默认传」决策）：JSON 对象（内联或 `@file`，可重复有序合并，后者同名键覆盖）顶层 assign 纯透传，效果与支持面由上游决定（网关 400 即时报错）；**保留字两级（v0.8.1 修订）**：硬保留 model/messages/stream/stream_options 与 headers 的 Authorization/Content-Type/Accept 直接 exit 1（防静默覆盖生命线字段/鉴权混乱）；软保留 reasoning_effort/max_tokens 允许经 extra 传入，但与 `-e`/`--max-output-tokens` 同时出现时**以快车道 flag 为准**（stderr 一行提示，覆盖可见可查）；`--extra-headers` 同构（默认头之后应用，User-Agent 允许覆盖）；`--dry-run` 展示合并结果；设计对齐 OpenAI SDK 的 extra_body/extra_headers 逃生口 |
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
| 界面语言 | 中英双语（zh/en）：`BRAIN_LANG` > `LC_ALL`/`LC_MESSAGES`/`LANG`（zh* → 中文，其他已设值 → 英文）> 时区偏移兜底（UTC+8 → 中文，否则英文；+8 区内无夏令时，按偏移判断免维护时区名清单）；语言随环境每次动态读取；stdout 模型正文永不翻译；Agent 分支以退出码与 --json 字段为准，不依赖 stderr 文案语言 |
| 多模型编排 | `brain compare`：同一问题**并行独立采样** ≥2 个模型，原文按分隔头拼接（`--json` 为 `responses[]`），裁决与合成留给宿主（宿主自身是模型，不内建 judge）；别名在 compare 语境下是**模型组快捷方式**——`-m reason` 展开为该链全部模型（与 ask 的链首位+容灾语义不同，帮助文案写明）；每腿为具体模型**不做故障转移**（静默转移摧毁「谁回答了什么」的透明性），同模型重试仍允许；部分失败语义同 `ping -m`：≥1 腿成功 exit 0、失败腿 stderr+`--json` 明示，全失败按最可行动码（2>3>4>5）；`--blind` 输出匿名 `Model_A/B/…`（真名映射走 stderr，防宿主裁决的品牌偏见） |
| 串行辩论（council） | `brain council -m … [-C 轮次，默认 2]`：2-N 个异构模型围绕固定上下文多轮讨论，裁决留宿主（同 compare 不内建裁判）。**混合拓扑**（M9，2026-10-26）：第 1 轮并行盲评（席位互不可见，消灭首轮锚定，复用 compare 并发）+ 第 2..C 轮蛇形串行（偶数轮反转，均衡先手/末位优势，C=2 时首列席位收尾）；发言记录扁平拼接递增、缺席留标记，**动态指令永远置于消息末尾**（严格前缀追加，命中网关 prompt caching）；每次发言告知席位与轮次，末轮为收尾指令（最终立场/观点变化/残余分歧）；`-C 1` 退化为串行 compare。**透镜=上下文**：席位-透镜表由调用方写进固定上下文（`-s` 追加在议事规则后），不做 persona 语法（`#` 等符号与真实模型 ID 撞车、短词语法表达力不足）；「虚拟伟人智囊团」提示词评审吸收三要素（透镜≠扮演、张力工程、末轮引向共识），协调员/整合者不搬；`-m` 去重（model_name=席位唯一 ID，同模型不占两席——同权重分饰两角=左右互搏）。失败语义：单元素链同模型重试、**无跨模型故障转移**（席位中途换人=跨轮身份造假）；重试仍败记缺席退出，有效席位 <2 提前终止但已积累发言照常输出（exit 0，沉没成本保护）；全失败按最可行动码（复用 compare 排序）；SIGINT dump 已积累发言 + exit 130。`--timeout` 为单次发言预算（墙钟 ≈ C×timeout，总调用 = N×C）；stdout 按 `===== model: X \| round r =====` 时间序拼接，`--json`：status（completed/partial/interrupted/failed）/usage 合计/transcript[]（每次发言含 usage/latency/error）；`[PASS]` 提前共识退出、`--blind` 见第 8 节缓办项 |

## 1. 目标与非目标

**目标**
- 给宿主 Agent 用的"外脑"：低摩擦、可脚本化、输出可解析、行为可预期。
- 开箱即用：一次 `brain config set`（或 `brain init`）后立即可用。
- 与宿主自带 subagent 机制**互补而非替代**：subagent = 同模型+工具+独立上下文（适合仓库级探索）；brain = 跨厂商模型+纯文本进出（适合独立第二意见，同源自我复核检不出系统性偏差）。对比表见 README（中/英两版均有该节）。

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
- [x] `--extra-body <json|@file>` / `--extra-headers <json|@file>`（M8）：JSON 对象通用透传口，可重复有序合并（后者同名键覆盖）；保留字两级见第 0 节「采样参数」行（硬保留拒绝、软保留快车道优先，v0.8.1）；ask/compare/council 共有（compare 每腿 / council 每次发言共用同一份）；`--dry-run` 展示合并结果
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
- [x] `brain compare`（M7）：`-m` 展开规则与失败语义见第 0 节「多模型编排」；无 `-m` 用 default_model 展开，展开后 <2 → exit 1；stdout 为成功腿的 `===== model: X =====` 分隔拼接（顺序=解析序而非完成序，失败腿不出现在 stdout，分隔头是语言中立的恒定契约不参与 i18n）；`--stream`/`--show-thinking` 不提供（多模型交错破坏 stdout 纯净）
- [x] `brain council`（M9）：拓扑/透镜/失败语义见第 0 节「串行辩论（council）」行；stdout 为成功发言的 `===== model: X | round r =====` 时间序拼接（缺席不出现在 stdout，发言记录内留缺席标记供后续席位可见、stderr 与 `--json` 明示）；`--stream`/`--show-thinking`/`--blind` 不提供（v1，见第 8 节）

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
- **M6**：界面双语（zh/en）
  - 状态（2026-10-26）：完成——CLI 全量（help/flag/错误/stderr/向导）+ README 中英双语（README.md 英文主版 + README.zh-CN.md，顶部互链）；检测链见第 0 节「界面语言」行；实现为 `internal/i18n` 的 `T(zh,en)/Tf` 双串直书，漏译即编译不出而非运行时缺文案；测试经 TestMain 钉 BRAIN_LANG=zh 保证既有断言稳定，英文路径有锁定测试
  - 设计取舍：拒绝按时区独判（语言是用户声明不是地理位置——欧美时区的中国开发者是核心受众）；时区仅作 locale 全空时的兜底且只认 UTC+8 偏移
- **M7**：`brain compare`（并行独立采样）+ `--blind` 匿名盲测 + README 动线重排（「为什么」前置：同源偏差一句 + 脏网关实测措辞）
  - 状态（2026-10-26）：完成——决策见第 0 节「多模型编排」；ask/compare 输入装配抽为共用 `assembleMessages`（行为不变，既有 ask 测试全绿）；单测覆盖 stdout 逐字节分隔头/展开去重/≥2 校验/部分失败/全失败 401→exit 2/盲测映射/dry-run 零请求
  - e2e（真实网关，2026-10-26）：部分失败实弹——cpa/gemini-3.8-flash 腿 model_not_found（清单列出≠可用，老问题）、sh/deepseek-v4.1-flash 腿成功 → exit 0 + 失败腿 stderr 明示 + `1/2 ok` 汇总；`--json --blind` 双腿（deepseek + fox/fable）成功：Model_A/B 匿名、stderr 真名映射、usage 逐腿；`--dry-run` 零请求
  - 来源：第三轮双外脑评审（fox/claude-opus-5-5 + cpa/gemini-4-argon-high，均针对「意义感」提问）唯一双收敛头牌——把项目自己实践的「双外脑交叉验证」内化为产品原语；两模型均主动与 backlog 的串行 `-m m1,m2` fallback 划清界限；Gemini 的内建 judge 提议被否（裁决留给宿主——宿主自身是模型），其 `--blind` 采纳；用户补充两个关键语义：别名=模型组快捷方式（一条 `-m` 携带整组）、每腿无故障转移（静默转移摧毁「谁回答了什么」的透明性）
- **M8**：`--extra-body` / `--extra-headers` 通用透传口
  - 状态（2026-10-26）：完成——保留字拒绝（body 生命线字段+专属 flag 字段、headers 鉴权字段）、`@file` 取值、可重复有序合并、`--dry-run` 可见合并结果；单测覆盖透传/合并/保留字/UA 覆盖/compare 全腿共用/畸形输入
  - e2e（真实网关，2026-10-26）：temperature 透传被接受；`response_format:json_object` 在 sh/deepseek-v4.1-flash 上真实生效（返回合法 JSON）；自定义 UA 请求成功；保留字拒绝零成本生效
  - 设计取舍：用户独立提出与 OpenAI SDK extra_body/extra_headers 同构的方案——一次性终结参数枚举军备竞赛，网关侧新参数/厂商私有字段永远无需跟版；`--output-schema` 专项 flag 取消（`--extra-body '{"response_format":…}'` 覆盖该场景）；此前讨论过的 `--json-object` 也无需独立 flag
  - v0.8.1 修订：`reasoning_effort`/`max_tokens` 从硬保留字改为**快车道优先**（用户决策：`-参数` 只做高频快车道，与 extras 同名时以 flag 为准 + stderr 提示）——拒绝清单只留生命线字段，逃生口不再拒绝合法参数
- **M9**：`brain council` 串行辩论（混合拓扑：首轮并行盲评 + 蛇形串行交锋）
  - 状态（2026-10-26）：**功能完成**，单测全绿（含 -race）——stdout 逐字节时间序拼接、首轮盲评互不可见、蛇形与 C=3 指令分层（增量轮/收尾轮）、缺席早期终止（status=partial）与中途缺席标记进入后续发言、全失败 401→exit 2、usage 合计、dry-run 蛇形调度表、互斥与轮次校验；决策见第 0 节「串行辩论（council）」行
  - e2e（真实网关，2026-10-26）：2 席位×2 轮实弹（deepseek 挂安全透镜 vs gemini 挂性能透镜，透镜经 `-s` 席位-透镜表注入）——首轮两席位确实各按透镜独立立论；次轮逐点交锋且各有真实自我修正（gemini 收回「网盘直同步」、deepseek 收回「进程边界即信任边界」等三处），末轮产出最终立场与未解分歧清单；194.9s、4/4 发言、exit 0、逐发言 usage 可见
  - 来源：用户设计（固定上下文+递增发言+轮次预算告知+原文交宿主裁决）× 双通道评审（本地 + `cpa/gemini-4-argon-high` 两轮独立）交叉收敛；混合拓扑/前缀缓存布局/强制交锋指令为 Argon 增量，透镜=上下文为三方收敛（Argon 自我推翻其 `-m "model#lens"` 提案）
  - 设计取舍：拒绝 per-seat fallback（席位换人=跨轮身份造假，compare 无转移先例延伸）；拒绝 `--max-tokens-per-turn` 默认值（违背纯透传，`--max-output-tokens` 现成）；拒绝 XML 输出标签（沿用 `=====` 家族契约）；拒绝 partial → exit 2（破坏 3.3 退出码表）；fyi.md「虚拟伟人智囊团」persona 评审吸收三要素（透镜≠扮演、张力工程、末轮引向共识→收尾指令），不搬伟人池/协调员/整合者（内建裁判）与状态机交互（flag 即交互协议）
- **并行工作流**：分发流水线（第 5 节）在 M1 期间就绪，M1 结束即可公开发布
  - 状态：**v0.1.0 已发布**（https://github.com/icheer/brain-cli/releases/tag/v0.1.0），6 平台产物 + SHA256SUMS，install.sh 全链路（下载→校验→运行）实测通过；CI 真实网关 e2e 的 secrets 待配置

## 8. 开放问题
1. **网关密钥发放**：若网关目前只有一把共享 key，公开分发前需先支持按用户发 key（网关侧配套工作，需确认可行性与时间点）
2. Windows 真机测试环境来源
3. **图像生成（`brain draw`）暂缓**（2026-10-26）：构想与设计草案已收敛——定位扩展为「外脑 = 咨询 + 生成」（均为单次无状态调用，不违背「不做 Function Calling」）；基建复用约九成（别名链/退出码/`--json`/心跳/分发）；关键取舍：draw 不回退 default_model（chat 语义），无 `-m` 时找 `image` 别名再无则报错；stdout 输出图片字节（`brain draw "..." > cat.png`，URL 形态自动下载后输出、b64 解码）；n=1 固定单张；`--size` 等 opt-in 纯透传。暂缓原因：实测本网关 new-api 对 `/v1/images/generations` 疑似把绘图请求转成聊天格式发给上游图像模型、上游 400（探测确认路由存在：503 model_not_found ≠ 404，但转换损坏只有实弹能暴露）——属网关侧问题，升级 new-api / 修正渠道路由后可重启
4. **`brain council`（串行辩论原语）已落地（2026-10-26，M9）**：决策移入第 0 节「串行辩论（council）」行；原三担忧的对策内化——成本超线性→混合拓扑 + C 默认 2 + 成本公式入 help/SKILL；序位效应→首轮盲评 + 蛇形轮换 + 不设指定总结者；中途失败→缺席退出 + 沉没成本保护。缓办项见第 6 条
5. **alias 角色预设（system/`-e`/max_tokens 绑进别名段）拒绝**（2026-10-26）：同一模型在不同场景下的预设是多变的，预设系统提示词没有稳定复用价值——`-s` 的临时性即正确形态（第三轮双外脑评审 Opus personas 独立段 / Gemini P2 角色钢印两个形态均被用户否决）。若未来出现真实复用需求再议
6. **council 缓办项**（2026-10-26）：`[PASS]` 提前共识退出（需标记解析协议且动摇固定预算可预测性，待真实使用暴露轮次浪费再议）；`--blind` 匿名（辩论语境的礼貌性收敛可能比 compare 更需要匿名，v1 未提供）；persona 语法（透镜=上下文已覆盖；`-m "model#lens"` 因符号与真实模型 ID 撞车、短词表达力不足被拒；同模型分饰正反方因 `-m` 去重语义不支持——同权重共享盲区，宿主可用单模型 ask + 圆桌提示词自演）

> 2026-10-24 已拍板并移入第 0 节：base_url 默认值（公开 release 不带）、fallback 触发范围、输出不做 `-o`、`--json` 失败语义、采样参数不透传、models.yaml 可选、工程骨架选型。
> 2026-10-26 已拍板并移入第 0 节：配置文件布局合并（单文件 config.yaml，原「models.yaml 可选」随之更名「aliases 段缺失」）。
