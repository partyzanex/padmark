package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"
)

func main() {
	if _, err := os.Stat("artifacts/index.html"); err != nil {
		log.Fatal("run make test-ui first: ", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:9323")
	if err != nil {
		log.Fatal(err)
	}
	address := "http://" + listener.Addr().String()
	fmt.Println("Browser test report:", address, "(Ctrl+C to stop)")
	var open *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		open = exec.Command("open", address)
	case "linux":
		open = exec.Command("xdg-open", address)
	case "windows":
		open = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	}
	if open != nil {
		if err := open.Run(); err != nil {
			log.Println("open the URL above:", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server := &http.Server{Handler: http.FileServer(http.Dir("artifacts")), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
