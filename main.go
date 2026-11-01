//go:build windows

// Intra opens websites as standalone desktop apps, with per-site DNS records
// and a certificate trust list that ignores the Windows certificate store.
package main

import (
	"intra/internal/app"
	"intra/internal/update"
)

var version = "1.0.0"

func main() {
	if !update.HandleArgs() {
		app.Run(version)
	}
}
