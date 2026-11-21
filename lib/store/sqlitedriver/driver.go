// Package sqlitedriver registers the SQLite database/sql driver.
//
// The server imports this package from its main executable. Client-only
// binaries must not import it so their cross-compilation does not pull in the
// platform-specific modernc/libc implementation.
package sqlitedriver

import (
	_ "modernc.org/sqlite"
)
