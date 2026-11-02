package lifecycle

import "sync/atomic"

type Readiness struct{ ready atomic.Bool }

func (r *Readiness) Set(value bool) { r.ready.Store(value) }
func (r *Readiness) Ready() bool    { return r.ready.Load() }
