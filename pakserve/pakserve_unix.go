//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func waitForSignal() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	for {
		<-c
		scanSearchPaths()
	}
}
