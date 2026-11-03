// Package instanceip contains shared value types for instance-owned fixed addresses.
// It has no cloud, storage or module dependencies.
package instanceip

import "time"

type Address struct {
	Kind               string            `json:"kind,omitempty"`
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	ARN                string            `json:"arn"`
	IP                 string            `json:"ip"`
	InstanceID         string            `json:"instance_id,omitempty"`
	NetworkInterfaceID string            `json:"network_interface_id,omitempty"`
	PrivateIP          string            `json:"private_ip,omitempty"`
	AssociationID      string            `json:"association_id,omitempty"`
	BorderGroup        string            `json:"border_group,omitempty"`
	Attached           bool              `json:"attached"`
	CreatedAt          *time.Time        `json:"created_at,omitempty"`
	Tags               map[string]string `json:"-"`
}
type Quota struct {
	Limit      float64 `json:"limit"`
	Used       float64 `json:"used"`
	LimitKnown bool    `json:"limit_known"`
	UsageKnown bool    `json:"usage_known"`
	Note       string  `json:"note,omitempty"`
}
type Automatic struct {
	Service          string `json:"service"`
	NewName          string `json:"new_name"`
	WavelengthZoneID string `json:"wavelength_zone_id,omitempty"`
	BorderGroup      string `json:"border_group,omitempty"`
	Quota            Quota  `json:"quota"`
}

// ManagedIP retains the allocation receipt used to prove instance ownership.
type ManagedIP struct {
	Address       Address `json:"address"`
	OwnerStepID   string  `json:"owner_step_id"`
	OwnerToken    string  `json:"owner_token"`
	OwnerSpecHash string  `json:"owner_spec_hash"`
	InstanceID    string  `json:"instance_id"`
	Service       string  `json:"service"`
}
