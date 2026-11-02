package contract

import "time"

type Runtime struct {
	Name string `json:"name"`
}

type SystemStatus struct {
	Name            string     `json:"name"`
	Version         string     `json:"version"`
	Revision        string     `json:"revision"`
	Timezone        string     `json:"timezone"`
	ServerTime      time.Time  `json:"server_time"`
	WorkerHeartbeat *time.Time `json:"worker_heartbeat"`
	WorkerHealthy   bool       `json:"worker_healthy"`
}
