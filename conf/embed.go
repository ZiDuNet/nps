package conf

import _ "embed"

// DefaultNpsConf is the server configuration template shipped with the
// application. Keeping the template in conf/nps.conf makes the checked-in
// example and the first-run generated file use the same source.
//
//go:embed nps.conf
var DefaultNpsConf string
