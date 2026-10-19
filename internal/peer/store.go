package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Peer is one paired daemon. Slice order in peers.json is the collision
// tie-break order (first-paired wins), so Load/Save preserve it.
type Peer struct {
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"added_at"`
}

// LoadPeers reads <dir>/peers.json. A missing file is (nil, nil): no peers.
func LoadPeers(dir string) ([]Peer, error) {
	data, err := os.ReadFile(filepath.Join(dir, "peers.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

// SavePeers writes <dir>/peers.json (0600 — it holds the trust roots for the
// peer channel) via temp file + rename so a crash never truncates it.
func SavePeers(dir string, peers []Peer) error {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "peers.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "peers.json"))
}
