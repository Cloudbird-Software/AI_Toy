// repoctl —— 元门禁 CLI 入口（spec §3.8）。子命令与退出码见 internal/repoctl 包文档。
package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/repoctl/internal/repoctl"
)

func main() {
	os.Exit(repoctl.Run(os.Args[1:], os.Stdout, os.Stderr))
}
