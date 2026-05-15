package master

import (
	"sync"
	"time"
)

type NodeMeta struct {
	NodeID    string     `json:"node_id"`
	Hostname  string     `json:"hostname,omitempty"`
	Tags      []string   `json:"tags,omitempty"`
	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	Expected  bool       `json:"expected,omitempty"`
}

type NodeDirectory struct {
	mu    sync.RWMutex
	nodes map[string]*NodeMeta
}

func NewNodeDirectory() *NodeDirectory {
	return &NodeDirectory{nodes: make(map[string]*NodeMeta)}
}

func (d *NodeDirectory) Observe(nodeID, hostname string, tags []string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		meta = &NodeMeta{NodeID: nodeID}
		d.nodes[nodeID] = meta
	}
	if meta.FirstSeen == nil {
		first := now
		meta.FirstSeen = &first
	}
	last := now
	meta.LastSeen = &last
	if hostname != "" {
		meta.Hostname = hostname
	}
	if tags != nil {
		meta.Tags = append([]string(nil), tags...)
	}
}

func (d *NodeDirectory) Touch(nodeID string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		return
	}
	last := now
	meta.LastSeen = &last
}

func (d *NodeDirectory) SetExpected(nodeID, hostname string, tags []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		meta = &NodeMeta{NodeID: nodeID}
		d.nodes[nodeID] = meta
	}
	meta.Expected = true
	if hostname != "" {
		meta.Hostname = hostname
	}
	if tags != nil {
		meta.Tags = append([]string(nil), tags...)
	}
}

func (d *NodeDirectory) UnsetExpected(nodeID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		return false
	}
	meta.Expected = false
	return true
}

const (
	DeleteOK = iota
	DeleteNotFound
	DeleteRefusedSeen
)

func (d *NodeDirectory) Delete(nodeID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		return DeleteNotFound
	}
	if meta.FirstSeen != nil {
		return DeleteRefusedSeen
	}
	delete(d.nodes, nodeID)
	return DeleteOK
}

func (d *NodeDirectory) Get(nodeID string) (NodeMeta, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	meta, ok := d.nodes[nodeID]
	if !ok {
		return NodeMeta{}, false
	}
	return copyMeta(*meta), true
}

func (d *NodeDirectory) List() []NodeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]NodeMeta, 0, len(d.nodes))
	for _, v := range d.nodes {
		out = append(out, copyMeta(*v))
	}
	return out
}

func (d *NodeDirectory) replaceAll(metas map[string]*NodeMeta) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodes = make(map[string]*NodeMeta, len(metas))
	for k, v := range metas {
		copied := copyMeta(*v)
		d.nodes[k] = &copied
	}
}

func (d *NodeDirectory) snapshotForPersist() map[string]*NodeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]*NodeMeta, len(d.nodes))
	for k, v := range d.nodes {
		copied := copyMeta(*v)
		out[k] = &copied
	}
	return out
}

func copyMeta(m NodeMeta) NodeMeta {
	if m.FirstSeen != nil {
		t := *m.FirstSeen
		m.FirstSeen = &t
	}
	if m.LastSeen != nil {
		t := *m.LastSeen
		m.LastSeen = &t
	}
	if m.Tags != nil {
		m.Tags = append([]string(nil), m.Tags...)
	}
	return m
}
