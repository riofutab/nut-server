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

type ShutdownMessage struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	DryRun    bool      `json:"dry_run,omitempty"`
	IssuedAt  time.Time `json:"issued_at"`
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
