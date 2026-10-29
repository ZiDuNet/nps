package controllers

import (
	"os"
	"strings"
)

// Clients lists the clients visible to the current principal.
func (s *ApiV1Controller) Clients() {
	s.emitLegacy(s.invokeSafe("client", "List", &ClientController{}, true))
}

// ClientsCreate creates a client. Ordinary user tokens are bound to their own
// account by the shared client action.
func (s *ApiV1Controller) ClientsCreate() {
	s.emitLegacy(s.invokeSafe("client", "Add", &ClientController{}, true))
}

// ClientGet returns one client DTO.
func (s *ApiV1Controller) ClientGet() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "GetClient", &ClientController{}, true))
}

// ClientUpdate patches a client. Missing fields keep their current values.
func (s *ApiV1Controller) ClientUpdate() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "Edit", &ClientController{}, true))
}

// ClientDelete removes a client and its tunnels/hosts.
func (s *ApiV1Controller) ClientDelete() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "Del", &ClientController{}, true))
}

// ClientStatus enables or disables a client (administrator only).
func (s *ApiV1Controller) ClientStatus() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "ChangeStatus", &ClientController{}, true))
}

// ClientUpgrade asks a connected NPC to run its built-in updater.
func (s *ApiV1Controller) ClientUpgrade() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "Upgrade", &ClientController{}, true))
}

// ClientUpdateCheck compares a client version with the latest stable release.
func (s *ApiV1Controller) ClientUpdateCheck() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("client", "UpdateCheck", &ClientController{}, false))
}

// Tunnels lists tunnels, optionally filtered by type and client.
func (s *ApiV1Controller) Tunnels() {
	s.emitLegacy(s.invokeSafe("index", "GetTunnel", &IndexController{}, true))
}

// TunnelsCreate creates a tunnel of any supported mode.
func (s *ApiV1Controller) TunnelsCreate() {
	s.emitLegacy(s.invokeSafe("index", "Add", &IndexController{}, true))
}

// TunnelGet returns one tunnel with its public access address.
func (s *ApiV1Controller) TunnelGet() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "GetOneTunnel", &IndexController{}, true))
}

// TunnelUpdate patches a tunnel.
func (s *ApiV1Controller) TunnelUpdate() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "Edit", &IndexController{}, true))
}

// TunnelDelete removes a tunnel.
func (s *ApiV1Controller) TunnelDelete() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "Del", &IndexController{}, true))
}

// TunnelStart starts a stopped tunnel.
func (s *ApiV1Controller) TunnelStart() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "Start", &IndexController{}, true))
}

// TunnelStop stops a running tunnel.
func (s *ApiV1Controller) TunnelStop() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "Stop", &IndexController{}, true))
}

// TunnelCopy duplicates a tunnel.
func (s *ApiV1Controller) TunnelCopy() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "Copy", &IndexController{}, true))
}

// Hosts lists domain reverse-proxy rules.
func (s *ApiV1Controller) Hosts() {
	s.emitLegacy(s.invokeSafe("index", "HostList", &IndexController{}, true))
}

// HostsCreate creates a domain reverse-proxy rule.
func (s *ApiV1Controller) HostsCreate() {
	s.emitLegacy(s.invokeSafe("index", "AddHost", &IndexController{}, true))
}

// HostGet returns one host DTO.
func (s *ApiV1Controller) HostGet() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "GetHost", &IndexController{}, true))
}

// HostUpdate patches a host rule.
func (s *ApiV1Controller) HostUpdate() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "EditHost", &IndexController{}, true))
}

// HostDelete removes a host rule.
func (s *ApiV1Controller) HostDelete() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "DelHost", &IndexController{}, true))
}

// HostStart enables a host rule.
func (s *ApiV1Controller) HostStart() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "HostStart", &IndexController{}, true))
}

// HostStop disables a host rule.
func (s *ApiV1Controller) HostStop() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("index", "HostStop", &IndexController{}, true))
}

// HostCheck reports whether a platform domain prefix is available.
func (s *ApiV1Controller) HostCheck() {
	s.emitLegacy(s.invokeSafe("index", "PlatformHostAvailable", &IndexController{}, true))
}

// HostDiagnose explains which rule matches a host, path and scheme.
func (s *ApiV1Controller) HostDiagnose() {
	s.emitLegacy(s.invokeSafe("index", "HostDiagnose", &IndexController{}, true))
}

// Users lists dashboard users (administrator only).
func (s *ApiV1Controller) Users() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("user", "List", &UserController{}, true))
}

// UsersCreate creates a dashboard user.
func (s *ApiV1Controller) UsersCreate() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("user", "Add", &UserController{}, true))
}

// UserUpdate patches a dashboard user.
func (s *ApiV1Controller) UserUpdate() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "Edit", &UserController{}, true))
}

// UserDelete removes a dashboard user.
func (s *ApiV1Controller) UserDelete() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "Del", &UserController{}, true))
}

// UserStatus enables or disables a dashboard user.
func (s *ApiV1Controller) UserStatus() {
	if !s.requireAdmin() {
		return
	}
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "ChangeStatus", &UserController{}, true))
}

// UserPassword resets a password (administrator) or changes the caller's own.
func (s *ApiV1Controller) UserPassword() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "ChangePassword", &UserController{}, true))
}

// UserToken generates/rotates the caller's or a target user's API token.
func (s *ApiV1Controller) UserToken() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "RegenerateAPIKey", &UserController{}, true))
}

// UserTokenRevoke revokes the caller's or a target user's API token.
func (s *ApiV1Controller) UserTokenRevoke() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "RevokeAPIKey", &UserController{}, true))
}

// UserTokenStatus reports whether the token is enabled and returns the stored
// credential when the caller still has permission to view it.
func (s *ApiV1Controller) UserTokenStatus() {
	s.pathFormValue("id")
	s.emitLegacy(s.invokeSafe("user", "APIKeyStatus", &UserController{}, false))
}

// GlobalConfig returns the parsed nps.conf with sensitive values masked.
func (s *ApiV1Controller) GlobalConfig() {
	if !s.requireAdmin() {
		return
	}
	content, err := os.ReadFile(serverConfigPath())
	if err != nil {
		s.apiFail(500, "config_unavailable", "读取 nps.conf 失败: "+err.Error())
		return
	}
	values := serverConfigKeyValues(string(content))
	masked := make(map[string]string, len(values))
	for key, value := range values {
		if serverConfigSensitive(key) && strings.TrimSpace(value) != "" {
			masked[key] = "••••••••"
			continue
		}
		masked[key] = value
	}
	s.apiData(map[string]interface{}{
		"path":   serverConfigPath(),
		"values": masked,
	})
}

// GlobalConfigSave writes the editable nps.conf keys.
func (s *ApiV1Controller) GlobalConfigSave() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "SaveConfig", &GlobalController{}, true))
}

// BridgeCertificate reports the single active/configured Bridge certificate
// without returning private-key material.
func (s *ApiV1Controller) BridgeCertificate() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "BridgeCertificate", &GlobalController{}, false))
}

// BridgeCertificateCheck validates certificate material without saving it.
func (s *ApiV1Controller) BridgeCertificateCheck() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "CheckBridgeCertificate", &GlobalController{}, true))
}

// BridgeCertificateSave atomically updates bridge_cert_file and
// bridge_key_file in nps.conf.
func (s *ApiV1Controller) BridgeCertificateSave() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "SaveBridgeCertificate", &GlobalController{}, true))
}

// GlobalRestart asks the system service manager to restart NPS.
func (s *ApiV1Controller) GlobalRestart() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "Restart", &GlobalController{}, true))
}

// GeoStatus reports the local geodata state.
func (s *ApiV1Controller) GeoStatus() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "GeoStatus", &GlobalController{}, false))
}

// GeoUpdate downloads and applies the latest geoip/geosite data.
func (s *ApiV1Controller) GeoUpdate() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "UpdateGeoData", &GlobalController{}, true))
}

// BanList lists login-protection records.
func (s *ApiV1Controller) BanList() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "Banlist", &GlobalController{}, true))
}

// BanUnban removes one login-protection record.
func (s *ApiV1Controller) BanUnban() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "Unban", &GlobalController{}, true))
}

// BanUnbanAll clears all login-protection records.
func (s *ApiV1Controller) BanUnbanAll() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "Unbanall", &GlobalController{}, true))
}

// BanClean removes expired login-protection records.
func (s *ApiV1Controller) BanClean() {
	if !s.requireAdmin() {
		return
	}
	s.emitLegacy(s.invokeSafe("global", "Banclean", &GlobalController{}, true))
}

// IPRegisterList lists short-lived IP registrations visible to the current
// credential. Administrators see every bucket; user tokens see only clients
// owned by that account.
func (s *ApiV1Controller) IPRegisterList() {
	s.emitLegacy(s.invokeSafe("global", "IPRegisterList", &GlobalController{}, true))
}

// IPRegisterRevoke revokes one visible IP registration.
func (s *ApiV1Controller) IPRegisterRevoke() {
	s.emitLegacy(s.invokeSafe("global", "IPRegisterRevoke", &GlobalController{}, true))
}

// IPRegisterClean removes expired registrations in the caller's scope.
func (s *ApiV1Controller) IPRegisterClean() {
	s.emitLegacy(s.invokeSafe("global", "IPRegisterClean", &GlobalController{}, true))
}

// IPRegisterClear resets registrations in the caller's scope.
func (s *ApiV1Controller) IPRegisterClear() {
	s.emitLegacy(s.invokeSafe("global", "IPRegisterClear", &GlobalController{}, true))
}

// Audit lists control-plane audit events visible to the current credential.
func (s *ApiV1Controller) Audit() {
	s.emitLegacy(s.invokeSafe("audit", "List", &AuditController{}, true))
}

// AuditConnections lists Bridge and proxy connection records visible to the
// current credential.
func (s *ApiV1Controller) AuditConnections() {
	s.emitLegacy(s.invokeSafe("audit", "Connections", &AuditController{}, true))
}

// invokeSafe converts invokeLegacy errors into the /api/v1 error envelope.
func (s *ApiV1Controller) invokeSafe(controllerName, actionName string, target interface{}, forcePost bool) *responseRecorder {
	recorder, err := s.invokeLegacy(controllerName, actionName, target, forcePost)
	if err != nil {
		s.apiFail(500, "internal_error", err.Error())
		return nil
	}
	return recorder
}
