// synthgen —— 合成数据生成注册器 CLI 入口（spec §3.7）。子命令与退出码见 internal/synthgen 包文档。
package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/synthgen/internal/synthgen"
)

func main() {
	os.Exit(synthgen.Run(os.Args[1:], os.Stdout, os.Stderr))
}
