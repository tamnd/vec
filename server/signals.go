package server

import (
	"os"
	"os/signal"
	"syscall"
)

// notifySignals wires SIGINT and SIGTERM to ch, the two signals that trigger a
// graceful shutdown (spec 16 §10.5).
func notifySignals(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
}
