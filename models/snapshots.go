package models

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

// ConnectionRevision excludes presentation and discovery metadata.
func ConnectionRevision(protocol, endpoint, apiKey, model string) uint64 {
	identity, _ := json.Marshal([]string{protocol, endpoint, apiKey, model})
	hash := sha256.Sum256(identity)
	return binary.BigEndian.Uint64(hash[:8])
}

// GetServiceSnapshot checks persisted connections even if registry publication failed.
func (m *Manager) GetServiceSnapshot(id string) (llm.Service, string, uint64, error) {
	lookup := m.lookupService
	if strings.HasPrefix(id, "provider:") {
		lookup = m.resolveProviderService
	}
	entry, err := lookup(id)
	if err != nil {
		return nil, "", 0, err
	}
	if entry.sourceID == "" && entry.source == SourceCustomLabel && m.db != nil {
		row, err := m.db.GetModel(context.Background(), id)
		if err != nil {
			return nil, "", 0, err
		}
		entry = m.customModelEntry(*row)
		if entry.service == nil {
			return nil, "", 0, fmt.Errorf("unsupported model: %s", id)
		}
	}
	return m.loggedService(entry), entry.sourceID, entry.serviceRevision, nil
}
