package master

import "nut-server/internal/protocol"

// shutdownStatusClass captures, per shutdown status, how the command state
// machine treats it. Centralizing the classification in one table keeps the
// predicates below from drifting apart and makes adding a new status a
// single-row change instead of a hunt through scattered switch statements.
type shutdownStatusClass struct {
	// complete: the node is no longer outstanding for commandComplete /
	// markTimedOutCommands purposes (it reached a terminal-ish state).
	complete bool
	// final: a settled *remote-confirmed* outcome used to gate local shutdown.
	// Timeout is intentionally excluded — it is the master's own guess, not a
	// slave confirmation, so the master must not power itself off on it alone.
	final bool
	// replayDone: the node needs no further replay. Only a confirmed Executed
	// stops replay; Failed/Timeout are retried on the next reconnect/rebroadcast.
	replayDone bool
}

var shutdownStatusClasses = map[string]shutdownStatusClass{
	protocol.ShutdownStatusAccepted:  {},
	protocol.ShutdownStatusExecuting: {},
	protocol.ShutdownStatusExecuted:  {complete: true, final: true, replayDone: true},
	protocol.ShutdownStatusFailed:    {complete: true, final: true},
	protocol.ShutdownStatusTimeout:   {complete: true},
}

func isCompleteShutdownStatus(status string) bool {
	return shutdownStatusClasses[status].complete
}

func isFinalShutdownStatus(status string) bool {
	return shutdownStatusClasses[status].final
}

func isReplayDoneStatus(status string) bool {
	return shutdownStatusClasses[status].replayDone
}
