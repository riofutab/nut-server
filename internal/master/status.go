package master

import (
	"sort"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) Status() protocol.StatusResponse {
	s.commandMu.Lock()
	activeCommandID := s.activeCommand
	localShutdown := s.localShutdownStatusLocked()
	commandCopy := make(map[string]*shutdownCommandState, len(s.commands))
	for commandID, state := range s.commands {
		copied := &shutdownCommandState{
			Message:     state.Message,
			TargetNodes: make(map[string]struct{}, len(state.TargetNodes)),
			NodeUpdates: make(map[string]protocol.ShutdownAckMessage, len(state.NodeUpdates)),
			TimeoutAt:   state.TimeoutAt,
			CompletedAt: state.CompletedAt,
		}
		for nodeID := range state.TargetNodes {
			copied.TargetNodes[nodeID] = struct{}{}
		}
		for nodeID, update := range state.NodeUpdates {
			copied.NodeUpdates[nodeID] = update
		}
		commandCopy[commandID] = copied
	}
	s.commandMu.Unlock()

	var activeSummary *protocol.CommandSummary
	if activeCommandID != "" {
		summary := summarizeCommand(commandCopy[activeCommandID])
		activeSummary = &summary
	}
	upsStatus := s.currentUPSStatus()

	clients := s.registry.List()
	clientMap := make(map[string]*Client, len(clients))
	for _, client := range clients {
		clientMap[client.NodeID] = client
	}

	directoryEntries := s.directory.List()
	metaByID := make(map[string]NodeMeta, len(directoryEntries))
	knownNodes := make(map[string]struct{})
	for _, meta := range directoryEntries {
		metaByID[meta.NodeID] = meta
		knownNodes[meta.NodeID] = struct{}{}
	}
	for _, command := range commandCopy {
		for nodeID := range command.TargetNodes {
			knownNodes[nodeID] = struct{}{}
		}
		for nodeID := range command.NodeUpdates {
			knownNodes[nodeID] = struct{}{}
		}
	}

	nodeIDs := make([]string, 0, len(knownNodes))
	for nodeID := range knownNodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	now := time.Now().UTC()
	offlineAfter := s.cfg.OfflineAfter.Duration

	nodes := make([]protocol.NodeStatus, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		node := protocol.NodeStatus{NodeID: nodeID, State: protocol.NodeStateNeverSeen}
		meta, hasMeta := metaByID[nodeID]
		if hasMeta {
			node.Hostname = meta.Hostname
			node.Tags = append([]string(nil), meta.Tags...)
			node.Expected = meta.Expected
			node.FirstSeen = meta.FirstSeen
			node.LastSeen = meta.LastSeen
		}
		if client, ok := clientMap[nodeID]; ok {
			node.Connected = true
			lastSeen := client.LastSeen()
			node.LastSeen = &lastSeen
			if node.Hostname == "" {
				node.Hostname = client.Hostname
			}
			if node.Tags == nil {
				node.Tags = append([]string(nil), client.Tags...)
			}
		}
		switch {
		case node.Connected:
			node.State = protocol.NodeStateOnline
		case node.LastSeen != nil && now.Sub(*node.LastSeen) <= offlineAfter:
			node.State = protocol.NodeStateOnline
		case node.LastSeen != nil:
			node.State = protocol.NodeStateOffline
		default:
			node.State = protocol.NodeStateNeverSeen
		}
		if update, ok := latestNodeUpdate(nodeID, commandCopy); ok {
			copied := update
			node.LastShutdown = &copied
		}
		nodes = append(nodes, node)
	}

	return protocol.StatusResponse{
		ShutdownIssued: s.shutdownIssued.Load(),
		ActiveCommand:  activeSummary,
		UPS:            upsStatus,
		LocalShutdown:  localShutdown,
		Nodes:          nodes,
	}
}
