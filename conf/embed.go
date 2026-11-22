package conf

import (
	_ "embed"
	"strings"
)

// DefaultNpsConf is the compact, deployable server bootstrap template.
//
//go:embed nps.conf
var DefaultNpsConf string

//go:embed server-options.ini
var serverOptions string

// ServerConfigDefaults describes both the boot keys and the advanced options
// for the Web editor. It is not written to a deployment's nps.conf.
var ServerConfigDefaults = DefaultNpsConf + "\n" + serverOptions

// IsBootstrapConfig identifies the shipped starter, even if an operator
// copied it to disk and edited its listener settings before the first start.
func IsBootstrapConfig(content string) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(content, "\ufeff"), "\n")
	return strings.TrimSpace(first) == "# NPS bootstrap v1"
}
