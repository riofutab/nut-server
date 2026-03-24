package master

import "sync"

type Registry struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]*Client)}
}

func (r *Registry) Set(client *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.clients[client.NodeID]; ok {
		_ = existing.Close()
	}
	r.clients[client.NodeID] = client
}

func (r *Registry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, nodeID)
}

func (r *Registry) RemoveIfMatch(nodeID string, client *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.clients[nodeID]
	if !ok || current != client {
		return
	}
	delete(r.clients, nodeID)
}

func (r *Registry) Get(nodeID string) (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	client, ok := r.clients[nodeID]
	return client, ok
}

func (r *Registry) List() []*Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clients := make([]*Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}
	return clients
}
