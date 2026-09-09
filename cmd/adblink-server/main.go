package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goldenduo/AdbLink/pkg/server"
	"github.com/goldenduo/AdbLink/pkg/web"
)

var (
	version = "1.2.0"
)

func main() {
	listenAddr := flag.String("listen", ":8888", "Control listen address for Android agents")
	webAddr := flag.String("web", ":9999", "Listen address for Web Dashboard and REST API (empty to disable)")
	advertiseHost := flag.String("host", "127.0.0.1", "Public hostname/IP to advertise for adb connect")
	portMin := flag.Int("port-min", 55550, "Minimum port to assign for ADB connections")
	portMax := flag.Int("port-max", 55599, "Maximum port to assign for ADB connections")
	token := flag.String("token", "", "Optional authentication token required from agents")
	gracePeriod := flag.Duration("grace", 30*time.Second, "Grace period to hold port reservation on disconnect")
	showVersion := flag.Bool("version", false, "Show version and exit")

	flag.Parse()

	if *showVersion {
		fmt.Printf("adblink-server v%s\n", version)
		os.Exit(0)
	}

	logger := log.New(os.Stdout, "[AdbLink-Server] ", log.LstdFlags|log.Lmsgprefix)

	fmt.Println(`
   ___       协调     __    _         __   
  / _ | ___  ___/ /  / /   (_)__  ___/ /__ 
 / __ |/ _ \/ _  /  / /__ / / _ \/ _  / _ \
/_/ |_/_//_/\_,_/  /____//_/_//_/\_,_/\___/
   ADB Reverse Tunneling & Device Gateway
	`)

	srvCfg := server.Config{
		ListenAddr:    *listenAddr,
		AdvertiseHost: *advertiseHost,
		PortMin:       *portMin,
		PortMax:       *portMax,
		Token:         *token,
		GracePeriod:   *gracePeriod,
		Logger:        logger,
	}

	srv, err := server.NewServer(srvCfg)
	if err != nil {
		logger.Fatalf("Failed to initialize server: %v", err)
	}

	if err := srv.Start(); err != nil {
		logger.Fatalf("Failed to start server: %v", err)
	}

	var webSrv *web.WebServer
	if *webAddr != "" {
		webSrv = web.NewWebServer(srv, *webAddr, logger)
		if err := webSrv.Start(); err != nil {
			logger.Printf("Failed to start web dashboard on %s: %v", *webAddr, err)
		}
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	logger.Println("Received termination signal, shutting down gracefully...")

	if webSrv != nil {
		_ = webSrv.Stop()
	}
	_ = srv.Stop()

	logger.Println("Server stopped cleanly.")
}
