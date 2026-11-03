// Package catalogue defines the freshness metadata of display-only cloud directories.
package catalogue

import "time"

type Freshness struct {
	UpdatedAt     time.Time `json:"updated_at"`
	RefreshAfter  time.Time `json:"refresh_after"`
	Cached        bool      `json:"cached"`
	Stale         bool      `json:"stale"`
	Refreshing    bool      `json:"refreshing"`
	RefreshFailed bool      `json:"refresh_failed"`
}

// RegionState is shared by region-management and ordinary region pickers.
// Selection eligibility remains owned by each page's service.
type RegionState struct {
	Code  string `json:"code"`
	State string `json:"state"`
}
