package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goldenduo/AdbLink/pkg/agent"
)

var (
	version = "1.0.0"
)

func main() {
	serverAddr := flag.String("server", "", "Remote AdbLink server address (e.g. 1.2.3.4:9000) [REQUIRED]")
	deviceID := flag.String("id", "", "Custom device ID (auto-detected from Android serial if empty)")
	model := flag.String("model", "", "Custom model name (auto-detected if empty)")
	target := flag.String("target", "127.0.0.1:5555", "Local adbd target address")
	requestPort := flag.Int("request-port", 0, "Request specific port on server (0 = auto-allocated)")
	token := flag.String("token", "", "Authentication token if server requires one")
	autoAdbd := flag.Bool("auto-adbd", true, "Automatically enable and restart local adbd over TCP if unreachable")
	retry := flag.Duration("retry", 2*time.Second, "Initial reconnect retry backoff")
	maxRetry := flag.Duration("max-retry", 30*time.Second, "Maximum reconnect retry backoff")
	showVersion := flag.Bool("version", false, "Show version and exit")

	flag.Parse()

	if *showVersion {
		fmt.Printf("adblink-agent v%s\n", version)
		os.Exit(0)
	}

	if *serverAddr == "" {
		fmt.Fprintf(os.Stderr, "Error: -server flag is required (e.g. -server 1.2.3.4:9000)\n\n")
		flag.Usage()
		os.Exit(1)
	}

	logger := log.New(os.Stdout, "[AdbLink-Agent] ", log.LstdFlags|log.Lmsgprefix)

	fmt.Println(`
   ___       协调     __    _         __   
  / _ | ___  ___/ /  / /   (_)__  ___/ /__ 
 / __ |/ _ \/ _  /  / /__ / / _ \/ _  / _ \
/_/ |_/_//_/\_,_/  /____//_/_//_/\_,_/\___/
       Android Native Reverse Proxy Agent
	`)

	agCfg := agent.Config{
		ServerAddr:       *serverAddr,
		DeviceID:         *deviceID,
		Model:            *model,
		LocalAdbAddr:     *target,
		RequestedPort:    *requestPort,
		Token:            *token,
		AutoAdbd:         *autoAdbd,
		RetryInterval:    *retry,
		MaxRetryInterval: *maxRetry,
		Logger:           logger,
	}

	ag := agent.NewAgent(agCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Println("Received termination signal, shutting down agent...")
		cancel()
		ag.Stop()
	}()

	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		logger.Fatalf("Agent terminated with error: %v", err)
	}

	logger.Println("Agent exited cleanly.")
}
