// toyjudge —— LLM-as-verifier 评审机 CLI 入口（spec §3.3）。子命令与退出码见 internal/toyjudge 包文档。
package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/judge/internal/toyjudge"
)

func main() { os.Exit(toyjudge.Main(os.Args[1:], os.Stdout, os.Stderr)) }
