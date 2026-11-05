package common

// ClientIdentity is an additive NPC control-channel report. It is requested
// only after the server has established a main control connection and only for
// clients that advertise support, so older NPCs never see a new protocol frame.
type ClientIdentity struct {
	InstanceID string   `json:"instance_id"`
	Hostname   string   `json:"hostname,omitempty"`
	OS         string   `json:"os,omitempty"`
	Arch       string   `json:"arch,omitempty"`
	Version    string   `json:"version,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	LocalIPs   []string `json:"local_ips,omitempty"`
}
