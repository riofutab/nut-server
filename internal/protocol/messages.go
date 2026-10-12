package protocol

import "time"

const (
	TypeRegister    = "register"
	TypeRegisterAck = "register_ack"
	TypePing        = "ping"
	TypePong        = "pong"
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
	// ShutdownStatusSimulated is reported instead of executed when the slave
	// handled the command in dry-run mode, so the master never mistakes a
	// simulated shutdown for a real one.
	ShutdownStatusSimulated = "simulated"
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
	DryRun   bool     `json:"dry_run,omitempty"`
	// ShutdownReady is the slave's startup self-check of its shutdown command
	// (nil when the check was skipped, e.g. in dry-run).
	ShutdownReady *bool `json:"shutdown_ready,omitempty"`
}

type RegisterAckMessage struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
	// IssuedToken is set when the master issued this node its own token; the
	// slave must store it and register with it from then on.
	IssuedToken string `json:"issued_token,omitempty"`
}

type PingMessage struct {
	SentAt time.Time `json:"sent_at"`
}

type PongMessage struct {
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
	Simulated       int                  `json:"simulated"`
	Failed          int                  `json:"failed"`
	Timeout         int                  `json:"timeout"`
	Outstanding     int                  `json:"outstanding"`
	Complete        bool                 `json:"complete"`
	CompletedAt     *time.Time           `json:"completed_at,omitempty"`
	LastNodeUpdates []ShutdownAckMessage `json:"last_node_updates,omitempty"`
}

type NodeStatus struct {
	NodeID        string   `json:"node_id"`
	Hostname      string   `json:"hostname"`
	Tags          []string `json:"tags,omitempty"`
	State         string   `json:"state"`
	Expected      bool     `json:"expected,omitempty"`
	DryRun        bool     `json:"dry_run,omitempty"`
	ShutdownReady *bool    `json:"shutdown_ready,omitempty"`
	// Enrolled reports whether the master holds an issued token for the node.
	Enrolled     bool                `json:"enrolled"`
	Connected    bool                `json:"connected"`
	FirstSeen    *time.Time          `json:"first_seen,omitempty"`
	LastSeen     *time.Time          `json:"last_seen,omitempty"`
	LastShutdown *ShutdownAckMessage `json:"last_shutdown,omitempty"`
}

const (
	NodeStateOnline    = "online"
	NodeStateOffline   = "offline"
	NodeStateNeverSeen = "never_seen"
)

type UPSStatusView struct {
	Target         string     `json:"target"`
	OnBattery      *bool      `json:"on_battery,omitempty"`
	BatteryCharge  *int       `json:"battery_charge,omitempty"`
	RuntimeMinutes *int       `json:"runtime_minutes,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
}

const (
	LocalShutdownPhaseIdle          = "idle"
	LocalShutdownPhaseWaitingRemote = "waiting_remote"
	LocalShutdownPhaseWaitExpired   = "wait_expired"
	LocalShutdownPhaseEmergency     = "emergency"
	LocalShutdownPhaseExecuting     = "executing"
	LocalShutdownPhaseCompleted     = "completed"
	LocalShutdownPhaseFailed        = "failed"
)

const (
	LocalShutdownTriggerRemoteComplete   = "remote_complete"
	LocalShutdownTriggerWaitExpired      = "wait_expired"
	LocalShutdownTriggerEmergencyRuntime = "emergency_runtime"
	LocalShutdownTriggerUPSUnreachable   = "ups_unreachable"
)

type LocalShutdownStatus struct {
	Enabled           bool       `json:"enabled"`
	Phase             string     `json:"phase"`
	CommandID         string     `json:"command_id,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	DeadlineAt        *time.Time `json:"deadline_at,omitempty"`
	Trigger           string     `json:"trigger,omitempty"`
	LastRebroadcastAt *time.Time `json:"last_rebroadcast_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	// CommandReady is the startup self-check of the local shutdown command
	// (absent when not checked, e.g. dry-run).
	CommandReady *bool `json:"command_ready,omitempty"`
}

// EpisodeStatus describes the current on-battery period, if any.
type EpisodeStatus struct {
	StartedAt     time.Time `json:"started_at"`
	CommandID     string    `json:"command_id,omitempty"`
	FiredPolicies []string  `json:"fired_policies,omitempty"`
}

type StatusResponse struct {
	ShutdownIssued bool                 `json:"shutdown_issued"`
	ActiveCommand  *CommandSummary      `json:"active_command,omitempty"`
	Episode        *EpisodeStatus       `json:"episode,omitempty"`
	UPS            *UPSStatusView       `json:"ups,omitempty"`
	LocalShutdown  *LocalShutdownStatus `json:"local_shutdown,omitempty"`
	Nodes          []NodeStatus         `json:"nodes"`
}
