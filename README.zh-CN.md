### 一句话安装——把这段发给你的 Agent

```text
帮我安装 brain 技能：https://github.com/icheer/brain-cli
```

Agent 会打开仓库，按其中的说明完成安装（技能目录自带 install.sh，装好二进制后跑 `brain ping` 自检）。未配置网关时 `ping` 会失败并给出修复提示，Agent 拿到提示自然会向你要 base_url 与 api_key——密钥由你亲手交给本机 Agent，只写入本机 0600 配置文件；避免向 Agent 发送明文密钥。

二进制当个会话即可调用；技能本体下次会话生效。手动步骤见下。