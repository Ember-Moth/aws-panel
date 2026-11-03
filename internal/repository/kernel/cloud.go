package kernel

import "context"

const CloudConfigurationLock int64 = 92842002

type BindingGuard interface {
	CheckBinding(context.Context, string, string) error
}
type BindingGuardFactory func(Connection) BindingGuard
type ReferenceGuard interface {
	InUse(context.Context, string, string) (bool, error)
}
type ReferenceGuardFactory func(Connection) ReferenceGuard
