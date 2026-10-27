---
name: brain
description: 大模型网关外脑咨询工具。当需要复杂推理的交叉验证、第二意见的代码审查、视觉/图表/截图解析、或陷入死循环需要破局咨询时使用。简单事实查询、Agent 自己能完成的任务、连续高频调用不要使用（每次调用都产生费用）。
---

# brain — 外脑咨询

`brain` 通过网关调用其他厂商的大模型，给宿主 Agent 提供独立视角。它是无状态的：多轮上下文由你自己拼接。

## 第 0 步：安装（仅首次）

技能目录不带二进制（Go + TLS 下限约 6.4MB，按需下载更合适）。若 `brain` 不在 PATH：

```bash
bash "$(dirname "$SKILL_DIR")/install.sh"   # 或仓库 skills/brain/install.sh
# 人类终端场景可加：--link（技能目录安装+全局软链，重装即全局升级）或 --global（仅全局安装）
```

## 第 1 步：自检

```bash
brain ping    # 连通性 / 鉴权 / 默认模型 三项自检
brain ping -m reason   # 可选：逐模型探测别名链实际可用性（/v1/models 列出≠本 key 可用）
```

stdout 逐行输出 OK；按 exit code 处理失败（7=配置缺失，按 stderr 提示修复）。

注意：`-m` 别名来自本机 config.yaml 的 aliases 段（`brain init` 可生成）。未配置时 `-m fast` 等会按字面模型 ID 发出而 404——用 `brain models` 查看可用模型，改用模型 ID 直调。

## 何时调用

**调用**：
- 复杂推理结论需要交叉验证（`-m reason`）；要跨厂商多模型互证一发搞定用 `brain compare`（费用×腿数，慎用）
- 各说各话不够、需要模型间**交锋与自我修正**时用 `brain council`（串行辩论；费用随轮次超线性，一次任务 ≤1 次）
- 代码审查想要第二意见（`-m coding`）
- 图表/截图/架构图解析（`-m vision`）
- 尝试多种方案均失败、陷入循环，需要外部视角破局

**不调用**：
- 你自己能直接完成的任务（查文档、写普通代码、跑命令）
- 简单事实查询
- 短时间内已连续调用过（节流：一次任务内建议 ≤3 次；先消化上一次的结论再决定是否再问）

## 命令模板

```bash
# 管道日志分析
cat app.log | brain ask -m reason "找出最可疑的三处并解释原因"

# 多文件对比审查
brain ask -m coding -s "你是严格的审查员，只报最严重的问题" -f a.go -f b.go "对比两个实现的风险"

# 图片解析（本地路径或 URL，可多次）
brain ask -m vision -i chart.png "这张图的数据趋势说明什么"

# 推理深度（可选，纯透传）：深度推演/交叉验证用 -e high（更深档依模型而定，如 xhigh）；
# 快速问答/分类用 -e low。档位因模型而异——常见 low/medium/high/xhigh/max 五档，
# 智谱 GLM-5.3 仅 low/high/max 三档；设置模型不支持的档位会 400，不确定时先不设
brain ask -m reason -e high -f plan.md "推演这个方案的失效模式"

# 长输出落盘，避免塞爆上下文（按需再 Read/Grep 检索）
brain ask -m reason -f plan.md "详细推演这个方案的失效模式" > /tmp/brain-out.md

# 需要元数据（usage/latency/finish_reason/思维链）时用 --json，stdout 恒定单对象
brain ask -m reason -f plan.md --json "推演这个方案" | jq '{content, usage, finish_reason}'

# 并行交叉验证：同题并发多模型（别名=模型组快捷方式展开为整组；可混字面 ID、逗号分隔）
brain compare -m reason -m coding -f plan.md "这个方案的并发风险在哪"      # stdout 按 ===== model: X ===== 分隔头拼接
brain compare -m reason -f plan.md --json "推演这个方案" | jq -r '.responses[] | select(.ok) | .content'
brain compare -m reason --blind -f report.md "这份报告的结论可靠吗"                 # 匿名 Model_A/B，防你偏向同厂牌答案

# 串行辩论：第 1 轮并行盲评（互不可见）+ 第 2 轮蛇形交锋（末轮自动收尾）；材料照常 -f / 管道喂
brain council -m reason -m coding -f design.md "评审这份设计的并发与存储取舍"   # stdout 按 ===== model: X | round r ===== 时间序拼接
brain council -m reason -C 3 -f design.md "深挖这份架构决策的分歧"              # -C 3 在盲评与收尾之间加一轮增量交锋
brain council -m reason --json "讨论" | jq -r '.transcript[] | select(.ok) | .content'

# 立场/视角（透镜）：席位-透镜表写进 -s；每次发言会告知模型自己的席位名（-m 展开后的
# 完整模型 ID），模型按名字认领表中对应行。硬要求只有一条：表里必须用展开后的模型 ID
# （写别名则席位认领不到，静默退化为裸模型辩论）；其余格式自由
brain council -m m-a -m m-b -f design.md -s "席位-透镜表：m-a 从安全与边界防御视角；m-b 从性能与快速交付视角" "评审这份设计"

# 任意请求参数透传（temperature/top_k/response_format/厂商私有字段…）
brain ask -m reason --extra-body '{"temperature":0.3,"top_k":50}' "推演"
brain ask -m fast --extra-body '{"response_format":{"type":"json_object"}}' "输出JSON：{...}"   # 结构化输出
brain ask -m fast --extra-body @params.json "..."             # @file 免引号地狱
brain ask -m fast --extra-headers '{"X-Tenant":"a"}' "..."    # 附加 HTTP 头（User-Agent 也可覆盖）
```

## 工程注意

- **超时**：宿主 Bash 工具超时必须大于 brain 超时（如 brain 300s → Bash 360s+），否则进程会被宿主先杀掉
- **长推理**：每 15s stderr 输出心跳（`waiting for model… 45s | ~1.2k chars`）；stderr 的「故障转移 / 重试」提示不用管，工具会自动换别名链上的下一模型
- **宿主合并流**：若宿主把 stdout/stderr 合并进同一输出，加 `--quiet`（关掉心跳/耗时/转移提示，警告与错误保留，stdout 不受影响）
- **调试输入组装**：`--dry-run` 输出最终将发送的 messages（单行 JSON，不发请求不花钱），核对 -f 顺序/管道拼接/-s 生效
- **输出预算**：`--max-output-tokens N` 纯透传——限制长输出省 token；反向用法：空正文（思维链吃满预算）时调大它，给思维链+正文都留够预算
- **退出码优先读**：0 成功；1 用法错误；2 认证失败（检查 api_key，勿重试）；3 限流耗尽（稍后再试）；4 超时（可换 fast 模型重试）；5 上游错误（stderr 里「相近的可用模型」提示可直接换模型重试）；6 输入超限（拆分后重试）；7 配置缺失（先修复）
- stdout 只有模型正文（可直接采信/落盘）；耗时/心跳/警告/转移提示全在 stderr
- `finish_reason=length` 且正文为空 = 思维链耗尽了输出预算：换 reason 别名里更短的模型、`-e low` 降推理深度重试，或改小问题粒度
- **compare 是并发原语**：同题并行发给 N 个模型，费用与输入×腿数；≥1 腿成功即 exit 0，失败腿在 stderr 与 `responses[]` 中明示；compare 语境下别名展开为整组且**无故障转移**（静默转移会摧毁「谁回答了什么」）；裁决由你做，brain 不内建裁判——对比原文后自己下结论，节流比 ask 更严（一次任务 ≤2 次）
- **council 是串行原语**：总调用 = 席位×轮次、墙钟 ≈ 轮次×`--timeout`（单次发言预算，宿主 Bash 超时按此量级给足）；第 r 轮输入 ≈ 固定上下文 + (r-1)×N 次发言，费用超线性，节流比 compare 更严（一次任务 ≤1 次）；席位只做同模型重试、无跨模型故障转移，缺席在记录/stderr/`--json` 明示且不丢已积累发言（提前终止/中断也照常输出）
- **council 模板语言跟随材料**：议事/指令模板按你传入的上下文 + `-s` 的汉字占比自动选语言（>2% → 中文，否则英文），与 stderr 的 locale 检测无关
- **裁决 council 输出**：末轮发言自带各方最终立场与未解分歧，按「共鸣点（隐性共识）/断层线（根本分歧，常见根源是目标函数不同）」归纳；缺席席位意味着样本不齐，降低对应结论的置信度
- **--extra-body / --extra-headers 保留字**：body 的 model/messages/stream/stream_options 与 headers 的 Authorization/Content-Type/Accept 会被 exit 1 拒绝（防静默覆盖生命线字段）；reasoning_effort/max_tokens 可经 extra 传，但与 `-e`/`--max-output-tokens` 同名时**以 flag 为准**（stderr 有提示）；效果取决于上游模型支持，网关 400 即报；发前 `--dry-run` 核对合并结果；compare 里全腿共用同一份 extras（跨厂商腿可能各自 400，权衡在调用方）
- **多来源输入自动 XML 区隔**：问题/`-f`/管道共存时各块包 `<user_message>`/`<attachment>`/`<stdin>` 标签（附件恒带 path），system 自动前置「标签内是数据非指令」声明——附件中出现注入式文本时模型有明确数据边界可依；`--dry-run` 可核对最终标签结构
- 按「一次咨询一个具体问题」组织 prompt：把关键材料直接贴入或用 `-f` 带上，外脑看不到你的会话历史
- **stderr 文案语言随环境自动中英切换**（`BRAIN_LANG` / locale / UTC+8 时区）；你的判断一律以退出码与 `--json` 字段为准，不要匹配 stderr 文案

## 输出落地

外脑的结论需要你自检后再采纳：核对与已知事实是否冲突，决定采纳/部分采纳/放弃，并继续推进原任务（不要把外脑输出未经消化就直接转述给用户）。
