package main

import (
	"context"
	"fmt"
	"os"

	"github.com/partyzanex/padmark/internal/infra/cli"
)

func main() {
	app := cli.NewApp()

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
