package main

import (
	"context"
	"fmt"
	"os"

	"github.com/partyzanex/padmark/internal/infra/cmd"
)

func main() {
	err := cmd.Run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
