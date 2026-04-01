package cli

import (
	"context"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func pingCommand() *urcli.Command {
	return &urcli.Command{
		Name:   "ping",
		Usage:  "Check server liveness and readiness",
		Action: pingAction,
	}
}

func pingAction(ctx context.Context, cmd *urcli.Command) error {
	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	ew := &errWriter{w: os.Stdout}
	ew.printf("server: %s\n", cmd.String(FlagURL))

	liveErr := checkLiveness(ctx, cl)
	if liveErr != nil {
		ew.printf("healthz: FAIL (%s)\n", liveErr)
	} else {
		ew.printf("healthz: OK\n")
	}

	readyErr := checkReadiness(ctx, cl)
	if readyErr != nil {
		ew.printf("readyz:  FAIL (%s)\n", readyErr)
	} else {
		ew.printf("readyz:  OK\n")
	}

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	return readyErr
}

func checkLiveness(ctx context.Context, cl *padmark.Client) error {
	err := cl.Healthz(ctx)
	if err != nil {
		return fmt.Errorf("healthz: %w", err)
	}

	return nil
}

func checkReadiness(ctx context.Context, cl *padmark.Client) error {
	res, err := cl.Readyz(ctx)
	if err != nil {
		return fmt.Errorf("readyz: %w", err)
	}

	switch typed := res.(type) {
	case *padmark.ReadyzOK:
		return nil
	case *padmark.ErrorResponse:
		return fmt.Errorf("service unavailable: %s", typed.Message)
	default:
		return fmt.Errorf("unexpected response type: %T", res)
	}
}
