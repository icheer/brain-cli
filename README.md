# brain-cli

**English** | [简体中文](README.zh-CN.md)

> The repo is named `brain-cli`; the binary command is **`brain`**.

`brain` is a minimalist CLI that acts as a "side brain" for host agents (Claude Code, Gemini CLI, etc.): it calls an LLM gateway via the OpenAI Chat Completions compatible protocol, performs one high-quality advisory/analysis call, and hands the result back to the agent cleanly.

## Highlights

- **Agent-friendly**: stdout carries only the model reply; metadata/warnings/heartbeat all go to stderr. Pipes / `-f` multi-file / `-i` image multimodal input work out of the box
- **Model alias routing**: `fast` / `reason` / `coding` / `vision` aliases plus automatic failover (retry with backoff, switch along the chain, noted on stderr) — agents never memorize model names
- **Optional reasoning effort & output budget**: `-e low|medium|high|…` and `--max-output-tokens N`, both passed through as-is (unset = model/gateway defaults; tiers are model-specific — e.g. Zhipu GLM-5.3 only has low/high/max)
- **Parseable**: exit codes 0-7 / 130 with full semantics; `--json` always emits exactly one object (jq parses unconditionally; `error.code` matches the exit code)
- **Robust transport**: SSE long connection (survives idle-link middleboxes) + 15s stderr heartbeat (local progress display, zero network traffic) + one overall budget (retries eat the same deadline)
- **Stateless**: multi-turn context is assembled by the caller
- **Bilingual UI (zh/en)**: chosen automatically via `BRAIN_LANG` > `LC_ALL`/`LC_MESSAGES`/`LANG` (`zh*` → Chinese, anything else set → English) > timezone fallback (UTC+8 → Chinese); override any time with `BRAIN_LANG=zh|en`
- **Cross-platform**: darwin / linux / windows × amd64 / arm64, released via goreleaser, ~9MB static binary

## 30-second start

`brain` is **BYOK** (Bring Your Own Key): it provides no model service itself. You need the address and key of any **OpenAI-compatible API** — not necessarily a "gateway"; official APIs work directly, for example:

| Service | base_url |
| --- | --- |
| OpenAI | `https://api.openai.com/v1` |
| OpenRouter (multi-vendor aggregator) | `https://openrouter.ai/api/v1` |
| DeepSeek | `https://api.deepseek.com/v1` |
| Zhipu | `https://open.bigmodel.cn/api/paas/v4` |

```bash
# Install (pick one)
bash skills/brain/install.sh        # skill scenario: download the binary into the skill dir from Releases (SHA256-verified)
# or grab your platform's package from https://github.com/icheer/brain-cli/releases
go install github.com/icheer/brain-cli@latest   # developers

# Configure (once; the key lives only in a local 0600 file, never in any repo)
brain init                                            # interactive wizard, or the three lines below:
brain config set base_url https://api.openai.com/v1   # any OpenAI-compatible service
brain config set api_key sk-xxxx
brain config set default_model gpt-5.6                # optional; see config.example.yaml for a full example

# First question
brain ping
brain ask -m fast "introduce yourself in one sentence"   # -m also accepts literal model IDs; -e low/medium/high… tunes reasoning effort (optional)
```

For agents: copy or link `skills/brain/` into `.claude/skills/` (or `.agents/skills/`); SKILL.md teaches the host agent when to call brain, how, and when not to.

## brain vs. host subagents

Host agents ship their own subagent mechanism (e.g. Claude Code's Task tool): same model, tool access, isolated context. brain **complements** it rather than replacing it — the crux is "swap in a different brain" vs. "spawn another copy of yourself":

| Dimension | brain (side-brain CLI) | host subagent |
| --- | --- | --- |
| Model | any external model on the gateway (cross-vendor — a genuinely independent second opinion) | same model as the host (self-review; blind spots correlate with the main thread) |
| Context | sees only the prompt you assemble (bring your own material / `-f`) | can read files and run commands to explore the repo (great for wide sweeps) |
| Side effects | zero — text in, text out, no tool privileges | inherits host tools (file I/O, command execution) |
| Contract | exit codes 0-7/130, `--json`, timeouts/budgets — all programmable | returns report text; lifecycle managed by the host |
| Billing | gateway key; per-call usage visible in `--json` | host subscription/API bill; granularity depends on the host |

**Each one's weaknesses**: brain cannot see your conversation or repo (underfed input gets off-target answers), depends on gateway availability, and cross-vendor behavioral differences (effort tiers, defaults) need probing; subagent review is same-source (the same model checking its own conclusions cannot catch systematic bias), and heavy parallelism inflates token spend.

**Rule of thumb**: "have a different vendor's model cross-check this conclusion / break out of a loop" → brain; "same-model parallel repo-scale exploration and summarization" → subagent. They compose: subagents gather material, brain adjudicates across models.

**Swapping brains doesn't stop at swapping models — the endpoint can be a domain agent**: in niches like information gathering, financial research, fact-checking and personal assistance, a growing number of standalone agents expose OpenAI-compatible endpoints with internal tool use of their own. To the caller it is all a black box: send in the relevant context, take back the result — how the remote side reasons and which tools it orchestrates is its business. The "no Function Calling" rule constrains brain itself (as a client it orchestrates no tools), never what sits behind the endpoint. Precisely because the contract is plain text in and out, bare models and full agents alike share the same exit-code / `--json` / alias / heartbeat conventions. One config points at a single base_url; to mix several endpoints, front them with an aggregating gateway (OpenRouter / the one-api family) or point `BRAIN_CONFIG` at another config file.

## Status & roadmap

**:rocket: v0.6.0 released** (M0–M6) — milestone details in [DESIGN.md](DESIGN.md) section 7 (in Chinese).

| Milestone | Scope | Status |
| --- | --- | --- |
| M0 (MVP) | config system + `ask` (args/stdin/`-f`/`-i`/`-s`) + alias resolution + exit codes + `brain models` | ✅ done |
| M1 | SSE transport + heartbeat + failover/retry + `--json` + `brain ping` + release pipeline | ✅ done |
| M2 | `--stream` / `--show-thinking` / `brain init` | ✅ done (Homebrew tap pending) |
| M3 | `-e` reasoning-effort passthrough / `--json` error details (type/retryable/upstream_status/attempted_models) / `--quiet` / `--dry-run` / `ping -m` chain probing / `init` alias generation | ✅ done |
| M4 | `--max-output-tokens` output budget passthrough / `BRAIN_DEFAULT_MODEL` env | ✅ done |
| M5 | single-file config: config.yaml + models.yaml merged (breaking — aliases moved into the `aliases:` section, `BRAIN_MODELS` removed) | ✅ done |
| M6 | bilingual UI (zh/en) via locale/`BRAIN_LANG`/UTC+8 timezone detection; README in both languages | ✅ done |
