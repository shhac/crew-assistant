package main

import (
	"context"
	"os"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/cli"
	"github.com/shhac/lib-agent-harness/session"
)

var version = "dev"

func main() {
	// The bridge is started by the model's CLI with a narrow environment, and
	// its output is the tool protocol itself; nothing of the CLI may run first.
	if len(os.Args) == 2 && os.Args[1] == app.ToolBridge {
		if err := session.RunBridge(context.Background(), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	cli.Run(version)
}
