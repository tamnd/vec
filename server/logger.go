package server

import (
	"log"
	"os"
	"strings"
)

// logger is a small leveled logger over the standard log package (spec 16 §8.4).
// The server keeps logging dependency-free; structured fields are formatted into
// the message rather than emitted as JSON.
type logger struct {
	level int
	out   *log.Logger
}

const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

// newLogger builds a logger at the named level (debug, info, warn, error).
func newLogger(level string) *logger {
	return &logger{
		level: parseLevel(level),
		out:   log.New(os.Stderr, "", log.LstdFlags|log.LUTC),
	}
}

func parseLevel(s string) int {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	default:
		return levelInfo
	}
}

func (l *logger) logf(level int, tag, format string, args ...any) {
	if l == nil || level < l.level {
		return
	}
	l.out.Printf(tag+" "+format, args...)
}

func (l *logger) debugf(format string, args ...any) { l.logf(levelDebug, "debug", format, args...) }
func (l *logger) infof(format string, args ...any)  { l.logf(levelInfo, "info", format, args...) }
func (l *logger) warnf(format string, args ...any)  { l.logf(levelWarn, "warn", format, args...) }
func (l *logger) errorf(format string, args ...any) { l.logf(levelError, "error", format, args...) }
