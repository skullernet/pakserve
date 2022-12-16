//go:build windows

package main

func waitForSignal() {
	<-(chan int)(nil)
}
