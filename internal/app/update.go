package app

import (
	"context"
	"errors"
	"time"

	"intra/internal/netx"
	"intra/internal/update"
)

type updateState struct {
	Checking    bool   `json:"checking"`
	Downloading bool   `json:"downloading"`
	Version     string `json:"version,omitempty"`
	UpToDate    bool   `json:"upToDate"`
}

func (a *App) checkForUpdates(w *Window) {
	if a.updateState.Checking || a.updateState.Downloading {
		return
	}
	a.updateState.Checking = true
	w.upToDate = false
	epoch := w.updateMenuEpoch
	a.pushAll()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		client := netx.TrustedHTTPClient(15 * time.Second)
		client.CheckRedirect = update.AllowRedirect
		defer client.CloseIdleConnections()
		release, err := update.Check(ctx, client, a.version)
		a.post(func() {
			a.updateState.Checking = false
			if err == nil || errors.Is(err, update.ErrUpToDate) {
				a.availableUpdate = release
				a.updateState.Version = ""
				if release != nil {
					a.updateState.Version = release.Version
				}
				if errors.Is(err, update.ErrUpToDate) && !w.closing && w.updateMenuEpoch == epoch &&
					w.menu != nil && w.menu.open && w.menu.kind == "about" {
					w.upToDate = true
				}
			}
			a.pushAll()
		})
	}()
}

func (w *Window) resetUpdateNotice() {
	w.upToDate = false
	w.updateMenuEpoch++
}

func (a *App) installUpdate() {
	if a.availableUpdate == nil || a.updateState.Checking || a.updateState.Downloading {
		return
	}
	release := *a.availableUpdate
	a.updateState.Downloading = true
	a.pushAll()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		client := netx.TrustedHTTPClient(3 * time.Minute)
		client.CheckRedirect = update.AllowRedirect
		defer client.CloseIdleConnections()
		prepared, err := update.Prepare(ctx, client, &release)
		if err == nil {
			err = prepared.Start()
			if err != nil {
				prepared.Cleanup()
			}
		}
		a.post(func() {
			if err != nil {
				// Failures stay quiet and leave the running program in place.
				a.updateState.Downloading = false
				a.pushAll()
				return
			}
			a.quit()
		})
	}()
}
