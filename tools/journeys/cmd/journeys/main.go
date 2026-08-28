// journeys 命令行入口（spec §3.5）；逻辑在 internal/journeys.Main，便于契约测试。
package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/journeys/internal/journeys"
)

func main() { os.Exit(journeys.Main(os.Args[1:], os.Stdout, os.Stderr)) }
