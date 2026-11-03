package kernel

import "context"

// ExecutionGuard participates in the caller's transaction. Holding its shared
// step lock prevents a stale worker from publishing another module's results.
type ExecutionGuard interface {
	CheckLease(context.Context, string, int64, string) error
}
type ExecutionGuardFactory func(Connection) ExecutionGuard
