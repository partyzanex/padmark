// Command report serves the browser-test report from artifacts/ on a local address and opens it
// in the default browser.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"
)

const (
	reportAddress   = "127.0.0.1:9323"
	shutdownTimeout = 5 * time.Second
	headerTimeout   = 5 * time.Second
)

func main() {
	os.Exit(run())
}

// run serves the report and returns the process exit code. It returns instead of calling
// log.Fatal so that the deferred cleanup always runs.
func run() int {
	_, err := os.Stat("artifacts/index.html")
	if err != nil {
		log.Println("run make test-ui first:", err)

		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", reportAddress)
	if err != nil {
		log.Println(err)

		return 1
	}

	address := "http://" + listener.Addr().String()
	log.Println("Browser test report:", address, "(Ctrl+C to stop)")
	openBrowser(ctx, address)

	//nolint:exhaustruct_v5 // the other http.Server fields keep their safe zero values
	server := &http.Server{
		Handler:           http.FileServer(http.Dir("artifacts")),
		ReadHeaderTimeout: headerTimeout,
	}

	go func() {
		<-ctx.Done()

		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		shutdownErr := server.Shutdown(shutdown)
		if shutdownErr != nil {
			log.Println("shut down the report server:", shutdownErr)
		}
	}()

	err = server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Println(err)

		return 1
	}

	return 0
}

// openBrowser asks the operating system to open the address. A failure only prints a hint.
func openBrowser(ctx context.Context, address string) {
	var open *exec.Cmd

	//nolint:gosec // G204: the address is the local listener address of this program
	switch runtime.GOOS {
	case "darwin":
		open = exec.CommandContext(ctx, "open", address)
	case "linux":
		open = exec.CommandContext(ctx, "xdg-open", address)
	case "windows":
		open = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", address)
	default:
		// No known opener on this system: the address is already printed for the user.
		return
	}

	err := open.Run()
	if err != nil {
		log.Println("open the URL above:", err)
	}
}
