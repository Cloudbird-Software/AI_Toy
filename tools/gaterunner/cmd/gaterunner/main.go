package main

import (
	"os"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/internal/gaterunner"
)

func main() {
	os.Exit(gaterunner.Main(os.Args[1:], os.Stdout, os.Stderr))
}
