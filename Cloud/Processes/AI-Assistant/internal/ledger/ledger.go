// Package ledger prevents replay after a retry or process restart. A request
// left pending by a crash is deliberately not re-run: its action may have run.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"smart-home/ai-assistant/internal/protocol"
)

type Record struct {
	Hash    string    `json:"hash"`
	Status  string    `json:"status"`
	Updated time.Time `json:"updated"`
}
type Store struct {
	root  string
	limit int
	ttl   time.Duration
}

func New(root string, limit int, ttl time.Duration) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Store{root, limit, ttl}, nil
}
func (s *Store) path(id string) string { return filepath.Join(s.root, strings.ToLower(id)+".json") }
func (s *Store) Begin(request protocol.Request) (bool, error) {
	if !protocol.UUID(request.RequestID) {
		return false, fmt.Errorf("invalid ledger request_id")
	}
	data, _ := json.Marshal(request)
	hash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(hash[:])
	old, err := os.ReadFile(s.path(request.RequestID))
	if err == nil {
		var record Record
		if json.Unmarshal(old, &record) != nil {
			return false, fmt.Errorf("invalid existing request record")
		}
		if record.Hash != fingerprint {
			return false, fmt.Errorf("request_id reused with different content")
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	if err := s.prune(); err != nil {
		return false, err
	}
	return true, s.write(request.RequestID, Record{Hash: fingerprint, Status: "pending", Updated: time.Now().UTC()})
}
func (s *Store) Finish(id, status string) error {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	record.Status, record.Updated = status, time.Now().UTC()
	return s.write(id, record)
}
func (s *Store) write(id string, record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.root, ".record-*")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(path, s.path(id))
}
func (s *Store) prune() error {
	files, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	type item struct {
		path    string
		updated time.Time
	}
	var retained []item
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.root, file.Name())
		info, err := file.Info()
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) > s.ttl {
			if err := os.Remove(path); err != nil {
				return err
			}
		} else {
			retained = append(retained, item{path, info.ModTime()})
		}
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].updated.Before(retained[j].updated) })
	for len(retained) >= s.limit {
		if err := os.Remove(retained[0].path); err != nil {
			return err
		}
		retained = retained[1:]
	}
	return nil
}
