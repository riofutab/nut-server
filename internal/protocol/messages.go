package protocol

import "time"

const (
	TypeRegister    = "register"
	TypeRegisterAck = "register_ack"
	TypePing        = "ping"
	TypeShutdown    = "shutdown"
	TypeShutdownAck = "shutdown_ack"
	TypeError       = "error"
)

const (
	ShutdownStatusAccepted  = "accepted"
	ShutdownStatusExecuting = "executing"
	ShutdownStatusExecuted  = "executed"
	ShutdownStatusFailed    = "failed"
	ShutdownStatusTimeout   = "timeout"
)

type Envelope struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
}

type RegisterMessage struct {
	NodeID   string   `json:"node_id"`
	Hostname string   `json:"hostname"`
	Token    string   `json:"token"`
	Tags     []string `json:"tags,omitempty"`
}

type RegisterAckMessage struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

type PingMessage struct {
	SentAt time.Time `json:"sent_at"`
}

type ShutdownTarget struct {
	All     bool     `json:"all,omitempty"`
	NodeIDs []string `json:"node_ids,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type ShutdownMessage struct {
	CommandID string         `json:"command_id"`
	Reason    string         `json:"reason"`
	DryRun    bool           `json:"dry_run,omitempty"`
	IssuedAt  time.Time      `json:"issued_at"`
	Target    ShutdownTarget `json:"target,omitempty"`
}

type ShutdownAckMessage struct {
	CommandID string    `json:"command_id"`
	NodeID    string    `json:"node_id"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	AckedAt   time.Time `json:"acked_at"`
}

type ErrorMessage struct {
	Message string `json:"message"`
}

type ShutdownRequest struct {
	Reason         string   `json:"reason"`
	NodeIDs        []string `json:"node_ids,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	DryRun         *bool    `json:"dry_run,omitempty"`
	TimeoutSeconds *int     `json:"timeout_seconds,omitempty"`
}

type CommandSummary struct {
	CommandID       string               `json:"command_id"`
	Reason          string               `json:"reason"`
	DryRun          bool                 `json:"dry_run"`
	IssuedAt        time.Time            `json:"issued_at"`
	TimeoutAt       *time.Time           `json:"timeout_at,omitempty"`
	Target          ShutdownTarget       `json:"target"`
	Targeted        int                  `json:"targeted"`
	Acknowledged    int                  `json:"acknowledged"`
	Executing       int                  `json:"executing"`
	Executed        int                  `json:"executed"`
	Failed          int                  `json:"failed"`
	Timeout         int                  `json:"timeout"`
	Outstanding     int                  `json:"outstanding"`
	Complete        bool                 `json:"complete"`
	CompletedAt     *time.Time           `json:"completed_at,omitempty"`
	LastNodeUpdates []ShutdownAckMessage `json:"last_node_updates,omitempty"`
}

type NodeStatus struct {
	NodeID       string              `json:"node_id"`
	Hostname     string              `json:"hostname"`
	Tags         []string            `json:"tags,omitempty"`
	Connected    bool                `json:"connected"`
	LastSeen     *time.Time          `json:"last_seen,omitempty"`
	LastShutdown *ShutdownAckMessage `json:"last_shutdown,omitempty"`
}

type StatusResponse struct {
	ShutdownIssued bool            `json:"shutdown_issued"`
	ActiveCommand  *CommandSummary `json:"active_command,omitempty"`
	Nodes          []NodeStatus    `json:"nodes"`
}
