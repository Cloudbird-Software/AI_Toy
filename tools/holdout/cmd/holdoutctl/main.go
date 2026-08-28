package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/holdout/internal/holdoutctl"
)

func main() { os.Exit(holdoutctl.Run(os.Args[1:])) }
