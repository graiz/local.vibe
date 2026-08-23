package peer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPeersRoundTripPreservesOrder(t *testing.T) {
	dir := t.TempDir()
	in := []Peer{
		{Name: "imac", Host: "imac.local", Port: 7444, Fingerprint: "aa", AddedAt: time.Now().UTC().Truncate(time.Second)},
		{Name: "studio", Host: "192.168.1.20", Port: 7444, Fingerprint: "bb", AddedAt: time.Now().UTC().Truncate(time.Second)},
	}
	if err := SavePeers(dir, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX-only: Windows has no permission bits — same guard as
	// internal/cert's key tests.
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0600 {
			t.Fatalf("peers.json mode = %v, want 0600", info.Mode().Perm())
		}
	}
	out, err := LoadPeers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "imac" || out[1].Name != "studio" {
		t.Fatalf("order not preserved: %+v", out)
	}
	if out[0].Fingerprint != "aa" || out[1].Host != "192.168.1.20" {
		t.Fatalf("fields lost: %+v", out)
	}
}

func TestLoadPeersMissingFile(t *testing.T) {
	out, err := LoadPeers(t.TempDir())
	if err != nil || out != nil {
		t.Fatalf("missing file: got (%v, %v), want (nil, nil)", out, err)
	}
}
