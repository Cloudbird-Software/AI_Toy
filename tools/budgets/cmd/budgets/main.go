// budgets —— 延迟预算台账 CLI 入口（spec §3.6）。子命令与退出码见 internal/budgets 包文档。
package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/budgets/internal/budgets"
)

func main() {
	os.Exit(budgets.Run(os.Args[1:], os.Stdout, os.Stderr))
}
