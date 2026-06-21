package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock() Clock {
	t := time.Date(2026, 6, 20, 14, 23, 1, 234567000, time.UTC)
	return func() time.Time { return t }
}

func TestLogLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, fixedClock())
	err := l.Log(Event{
		Event:      EventDataDelete,
		Principal:  "api-key:analytics-service",
		Collection: "user_profiles",
		Op:         "DELETE",
		Count:      1,
		Filter:     "id = 12345",
		LSN:        9981234,
		DurationUS: 142,
		OK:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("line must end with newline")
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("want exactly one newline, got %d", strings.Count(out, "\n"))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v", err)
	}
	if got["ts"] != "2026-06-20T14:23:01.234567Z" {
		t.Fatalf("bad ts: %v", got["ts"])
	}
	if got["level"] != "AUDIT" {
		t.Fatalf("bad level: %v", got["level"])
	}
	if got["event"] != "data.delete" {
		t.Fatalf("bad event: %v", got["event"])
	}
	if _, ok := got["reason"]; ok {
		t.Fatal("empty reason must be omitted")
	}
}

func TestLogDenyHasNoDataFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, fixedClock())
	_ = l.Log(Event{
		Event:      EventAuthDeny,
		Principal:  "jwt:user-abc",
		Op:         "WRITE",
		Collection: "private_docs",
		Reason:     "no_binding",
	})
	var got map[string]any
	_ = json.Unmarshal(buf.Bytes(), &got)
	if got["reason"] != "no_binding" {
		t.Fatalf("missing reason: %v", got)
	}
	if _, ok := got["count"]; ok {
		t.Fatal("deny event must not carry a count")
	}
	if _, ok := got["lsn"]; ok {
		t.Fatal("deny event must not carry an lsn")
	}
}

func TestOpenAppendsWithMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vec-audit.log")
	l, err := Open(path, fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Log(Event{Event: EventServerStart, Version: "0.1.0"})
	_ = l.Close()

	l2, err := Open(path, fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	_ = l2.Log(Event{Event: EventServerStop})
	_ = l2.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want mode 0600, got %o", info.Mode().Perm())
	}

	f, _ := os.Open(path)
	defer f.Close()
	var lines int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines++
		if !json.Valid(sc.Bytes()) {
			t.Fatalf("line %d invalid JSON: %s", lines, sc.Text())
		}
	}
	if lines != 2 {
		t.Fatalf("want 2 appended lines, got %d", lines)
	}
}

func TestConcurrentLogLinesAreIntact(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, fixedClock())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Log(Event{Event: EventDataInsert, Collection: "c", Count: 1})
		}()
	}
	wg.Wait()
	sc := bufio.NewScanner(&buf)
	var lines int
	for sc.Scan() {
		lines++
		if !json.Valid(sc.Bytes()) {
			t.Fatalf("interleaved line: %s", sc.Text())
		}
	}
	if lines != 50 {
		t.Fatalf("want 50 lines, got %d", lines)
	}
}

func TestDiscard(t *testing.T) {
	l := Discard()
	if err := l.Log(Event{Event: EventServerStart}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
