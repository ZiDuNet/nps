//go:build !linux

package fnos

// fnOS execution is rejected on these platforms. Keep shared NPC builds and
// pure config tests available without importing Linux-only syscall constants.
func lockLog(string) (func(), error) { return func() {}, nil }
