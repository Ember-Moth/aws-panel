package state

import (
	"sync/atomic"

	"github.com/Ember-Moth/aws-panel/internal/module/platform/contract"
)

// Runtime publishes immutable settings after bootstrap has loaded the database.
type Runtime struct {
	value atomic.Pointer[contract.Runtime]
}

func (r *Runtime) Read() contract.Runtime {
	if value := r.value.Load(); value != nil {
		return *value
	}
	return contract.Runtime{}
}

func (r *Runtime) Publish(value contract.Runtime) { r.value.Store(&value) }
