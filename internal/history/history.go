package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	maxRecent = 5
	appDir    = ".aws-ssm-connect"
	fileName  = "history.json"
)

// Entry represents a recently connected instance.
type Entry struct {
	InstanceID string    `json:"instance_id"`
	Name       string    `json:"name,omitempty"`
	LastUsed   time.Time `json:"last_used"`
}

// History manages recently connected instances.
type History struct {
	Recent []Entry `json:"recent"`
	path   string
}

// Load reads history from ~/.aws-ssm-connect/history.json.
func Load() (*History, error) {
	h := &History{}

	home, err := os.UserHomeDir()
	if err != nil {
		return h, nil // Return empty history on error
	}

	h.path = filepath.Join(home, appDir, fileName)

	data, err := os.ReadFile(h.path)
	if err != nil {
		return h, nil // Return empty history if file doesn't exist
	}

	_ = json.Unmarshal(data, h)
	return h, nil
}

// Add records a connection to an instance.
func (h *History) Add(instanceID, name string) error {
	// Remove existing entry for this instance
	filtered := make([]Entry, 0, len(h.Recent))
	for _, e := range h.Recent {
		if e.InstanceID != instanceID {
			filtered = append(filtered, e)
		}
	}

	// Add new entry at the front
	h.Recent = append([]Entry{{
		InstanceID: instanceID,
		Name:       name,
		LastUsed:   time.Now(),
	}}, filtered...)

	// Keep only maxRecent entries
	if len(h.Recent) > maxRecent {
		h.Recent = h.Recent[:maxRecent]
	}

	return h.save()
}

// RecentIDs returns instance IDs in order of most recent use.
func (h *History) RecentIDs() []string {
	ids := make([]string, len(h.Recent))
	for i, e := range h.Recent {
		ids[i] = e.InstanceID
	}
	return ids
}

func (h *History) save() error {
	if h.path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		h.path = filepath.Join(home, appDir, fileName)
	}

	// Create directory if needed
	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(h.path, data, 0600)
}
