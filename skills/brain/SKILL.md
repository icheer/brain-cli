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
- 复杂推理结论需要交叉验证（`-m reason`）
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
brain ask -m reason -e high "推演方案A的风险"

# 长输出落盘，避免塞爆上下文（按需再 Read/Grep 检索）
brain ask -m reason "详细推演方案A的风险" > /tmp/brain-out.md

# 需要元数据（usage/latency/finish_reason/思维链）时用 --json，stdout 恒定单对象
brain ask -m reason --json "推演方案" | jq '{content, usage, finish_reason}'
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
- 按「一次咨询一个具体问题」组织 prompt：把关键材料直接贴入或用 `-f` 带上，外脑看不到你的会话历史

## 输出落地

外脑的结论需要你自检后再采纳：核对与已知事实是否冲突，决定采纳/部分采纳/放弃，并继续推进原任务（不要把外脑输出未经消化就直接转述给用户）。
