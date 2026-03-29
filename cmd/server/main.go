package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/partyzanex/padmark/internal/infra/cmd"
)

func main() {
	app := cmd.NewApp()

	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
