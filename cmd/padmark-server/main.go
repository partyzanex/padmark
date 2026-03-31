package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/partyzanex/padmark/internal/infra/server"
)

func main() {
	app := server.NewApp()

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		slog.Error("run", "err", err)
		os.Exit(1)
	}
}
