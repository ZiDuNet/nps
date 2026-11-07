package common

// UpdateCapability is additive to CLIENT_IDENTITY. Protocol 2 is negotiated by
// capability, not a product version guess; old peers never receive upv2 frames.
type UpdateCapability struct {
	Protocol int    `json:"protocol"`
	Kind     string `json:"kind"`
	Mode     string `json:"mode"`
	Enabled  bool   `json:"enabled"`
	Reason   string `json:"reason,omitempty"`
}

type ClientUpdateRequest struct {
	JobID   string `json:"job_id"`
	Version string `json:"version"`
}

type ClientUpdateProgress struct {
	JobID   string `json:"job_id"`
	Stage   string `json:"stage"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

const ClientUpdateHealthPrefix = "nps:update:v2:"
