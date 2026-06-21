package bulk

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDirDestinationPutGet(t *testing.T) {
	dst, err := NewDirDestination(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	payload := []byte("hello segment")
	if err := dst.Put("generations/g1/segments/0001.seg", bytes.NewReader(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, err := dst.Get("generations/g1/segments/0001.seg")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %q", got)
	}
}

func TestDirDestinationGetMissing(t *testing.T) {
	dst, err := NewDirDestination(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := dst.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDirDestinationList(t *testing.T) {
	dst, err := NewDirDestination(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	keys := []string{
		"generations/g1/segments/0002.seg",
		"generations/g1/segments/0001.seg",
		"generations/g2/segments/0001.seg",
		"generations.json",
	}
	for _, k := range keys {
		if err := dst.Put(k, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	got, err := dst.List("generations/g1/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{
		"generations/g1/segments/0001.seg",
		"generations/g1/segments/0002.seg",
	}
	if len(got) != len(want) {
		t.Fatalf("list count: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("list[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestDirDestinationDeleteIdempotent(t *testing.T) {
	dst, err := NewDirDestination(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := dst.Put("a/b", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := dst.Delete("a/b"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Deleting a missing key is not an error.
	if err := dst.Delete("a/b"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestDirDestinationEscapeGuard(t *testing.T) {
	root := t.TempDir()
	dst, err := NewDirDestination(root)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// A leading ../ is normalized back under the root rather than escaping it. The
	// object must land inside root, and a sibling file outside root must not appear.
	if err := dst.Put("../escape", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err != nil {
		t.Fatalf("expected normalized object under root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("object escaped the destination root")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dst, err := NewDirDestination(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Empty manifest when none exists.
	m, err := ReadManifest(dst)
	if err != nil {
		t.Fatalf("read empty: %v", err)
	}
	if len(m.Generations) != 0 {
		t.Fatalf("expected empty manifest, got %d generations", len(m.Generations))
	}

	m.Generations = append(m.Generations, Generation{
		ID:              "00000000000001",
		BaseVersion:     100,
		CreatedUnixNano: 1700000000000000000,
		Segments: []SegmentRef{
			{Key: SegmentKey("00000000000001", 110, false), BaseVersion: 100, EndVersion: 110},
			{Key: SegmentKey("00000000000001", 120, true), BaseVersion: 111, EndVersion: 120, Compressed: true},
		},
	})
	if err := WriteManifest(dst, m); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := dst.Get("generations.json"); err != nil {
		t.Fatalf("manifest object missing: %v", err)
	}

	back, err := ReadManifest(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if back.FormatVersion != manifestFormatVersion {
		t.Fatalf("format version: got %d", back.FormatVersion)
	}
	g, ok := back.Generation("00000000000001")
	if !ok {
		t.Fatal("generation not found after round trip")
	}
	if g.BaseVersion != 100 || len(g.Segments) != 2 {
		t.Fatalf("generation mismatch: %+v", g)
	}
	latest, ok := back.Latest()
	if !ok || latest.ID != "00000000000001" {
		t.Fatalf("latest: %+v ok=%v", latest, ok)
	}
}

func TestSegmentsCovering(t *testing.T) {
	g := Generation{
		BaseVersion: 100,
		Segments: []SegmentRef{
			{BaseVersion: 100, EndVersion: 110},
			{BaseVersion: 111, EndVersion: 120},
			{BaseVersion: 121, EndVersion: 130},
		},
	}
	got := g.SegmentsCovering(115)
	if len(got) != 2 {
		t.Fatalf("covering 115: got %d segments, want 2", len(got))
	}
	if got[1].EndVersion != 120 {
		t.Fatalf("covering 115: last segment end=%d, want 120", got[1].EndVersion)
	}
	all := g.SegmentsCovering(999)
	if len(all) != 3 {
		t.Fatalf("covering 999: got %d, want 3", len(all))
	}
}

func TestSegmentKey(t *testing.T) {
	k := SegmentKey("gen1", 42, false)
	if filepath.Base(k) != "00000000000000000042.seg" {
		t.Fatalf("key: %q", k)
	}
	kc := SegmentKey("gen1", 42, true)
	if filepath.Base(kc) != "00000000000000000042.seg.gz" {
		t.Fatalf("compressed key: %q", kc)
	}
}
