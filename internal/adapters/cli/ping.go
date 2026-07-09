package cli

import (
	"context"
	"errors"
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
	padmarkClient, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	erw := &errWriter{w: os.Stdout}
	erw.printf("server: %s\n", resolveServerURL(cmd))

	liveErr := checkLiveness(ctx, padmarkClient)
	if liveErr != nil {
		erw.printf("healthz: FAIL (%s)\n", liveErr)
	} else {
		erw.printf("healthz: OK\n")
	}

	readyErr := checkReadiness(ctx, padmarkClient)
	if readyErr != nil {
		erw.printf("readyz:  FAIL (%s)\n", readyErr)
	} else {
		erw.printf("readyz:  OK\n")
	}

	if erw.err != nil {
		return fmt.Errorf("write output: %w", erw.err)
	}

	// Both probes contribute to the exit code: a liveness failure must not be masked by a
	// passing readiness check. errors.Join returns nil only when both checks succeeded.
	return errors.Join(liveErr, readyErr)
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
