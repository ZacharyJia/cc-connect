package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ZacharyJia/cx-connect/dashboard"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7390", "listen address")
	token := flag.String("token", "", "optional bearer token for instance reports")
	ttl := flag.Duration("ttl", 20*time.Second, "offline threshold for instances")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	server := dashboard.NewServer(dashboard.ServerConfig{
		Listen:      *listen,
		Token:       *token,
		InstanceTTL: *ttl,
	})

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "cx-board failed: %v\n", err)
		os.Exit(1)
	}
}
