package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/saaskit-dev/acp-runtime-go/simulator"
)

func main() {
	var authMode string
	var storageDir string
	var name string
	var title string
	var version string
	flag.StringVar(&authMode, "auth-mode", "none", "authentication mode: none, optional, required")
	flag.StringVar(&storageDir, "storage-dir", "", "session storage directory")
	flag.StringVar(&name, "name", "", "agent implementation name")
	flag.StringVar(&title, "title", "", "agent title")
	flag.StringVar(&version, "version", "", "agent version")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := simulator.RunStdio(ctx, os.Stdin, os.Stdout, simulator.Options{
		AuthMode:   simulator.AuthMode(authMode),
		StorageDir: storageDir,
		Name:       name,
		Title:      title,
		Version:    version,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
