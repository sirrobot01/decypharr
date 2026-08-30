package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/sirrobot01/decypharr/internal/config"

	"github.com/sirrobot01/decypharr/cmd/decypharr"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("FATAL: Recovered from panic in main: %v\n", r)
			debug.PrintStack()
		}
	}()

	// Subcommands are dispatched before flag parsing, which would otherwise
	// stop at the subcommand name and treat it as a positional argument.
	if len(os.Args) > 1 && os.Args[1] == downgradeCommand {
		if err := runDowngrade(os.Args[2:]); err != nil {
			log.Fatalf("%s: %v", downgradeCommand, err)
		}
		return
	}

	var configPath string
	var pprofAddr string

	// Create a default config directory if it doesn't exist
	flag.StringVar(&configPath, "config", "", "path to the data folder")
	flag.StringVar(&pprofAddr, "pprof", ":6060", "pprof server address (set to empty to disable)")
	flag.Parse()

	// get enable pprof flag from environment variable if not set via flag
	enablePprof := os.Getenv("ENABLE_PPROF") != ""

	config.SetConfigPath(resolveConfigPath(configPath))
	config.Get()

	// Buffer pools are owned by their subsystems: the DFS cache (vfs.NewCache)
	// and the usenet reader each create a buffer.Pool with their own configured
	// RAM budget and disk limit.

	// Start pprof server if enabled
	if pprofAddr != "" && enablePprof {
		go func() {
			log.Printf("Starting pprof server on %s", pprofAddr)
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				log.Printf("pprof server error: %v", err)
			}
		}()
	}

	// Create a context canceled on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := decypharr.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
