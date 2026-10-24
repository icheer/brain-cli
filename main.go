// brain：给宿主 Agent 用的大模型网关外脑 CLI。
//
// stdout 只输出模型正文；警告/心跳/耗时走 stderr（TODO.md 3.2）。
package main

import "github.com/icheer/brain-cli/cmd"

func main() {
	cmd.Execute()
}
