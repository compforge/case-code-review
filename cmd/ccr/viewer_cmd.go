package main

import (
	"fmt"

	"github.com/qiankunli/case-code-review/internal/viewer"
)

type viewerOptions struct {
	addr     string
	showHelp bool
}

func parseViewerFlags(args []string) (viewerOptions, error) {
	a := newOcrFlagSet("ccr viewer")

	opts := viewerOptions{}
	a.StringVar(&opts.addr, "addr", "localhost:5483", "listen address")

	if err := a.Parse(args); err != nil {
		return opts, fmt.Errorf("parse flags: %w", err)
	}

	opts.showHelp = a.showHelp
	return opts, nil
}

func runViewer(args []string) error {
	opts, err := parseViewerFlags(args)
	if err != nil {
		return err
	}
	if opts.showHelp {
		printViewerUsage()
		return nil
	}

	fmt.Printf("Open Code Review Viewer starting on %s\n", viewer.BrowserURL(opts.addr))
	return viewer.StartServer(opts.addr)
}

func printViewerUsage() {
	fmt.Println(`Session history WebUI viewer.

Usage:
  ccr viewer [flags]

Flags:
  --addr <address>           listen address (default: localhost:5483)

Examples:
  ccr viewer                         # start on default port
  ccr viewer --addr 127.0.0.1:3000   # listen on port 3000`)
}
