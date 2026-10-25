package bridge

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// SelectMode controls how duplicate NPC sessions sharing one VerifyKey are
// selected. The current server keeps one authenticated data pair per client;
// the setting is still parsed and exposed so deployments can carry the same
// configuration across rolling upgrades without silently rejecting it.
type SelectMode int32

const (
	Primary SelectMode = iota
	RoundRobin
	Random
)

var clientSelectMode atomic.Int32

func SetClientSelectMode(value string) error {
	mode := Primary
	switch text := strings.ToLower(strings.TrimSpace(value)); text {
	case "", "primary", "p", "0":
		mode = Primary
	case "roundrobin", "round-robin", "round", "rr", "1":
		mode = RoundRobin
	case "random", "rand", "2":
		mode = Random
	default:
		if number, err := strconv.Atoi(text); err == nil && number >= int(Primary) && number <= int(Random) {
			mode = SelectMode(number)
		} else {
			clientSelectMode.Store(int32(Primary))
			return fmt.Errorf("无效的 bridge_select_mode=%q", value)
		}
	}
	clientSelectMode.Store(int32(mode))
	return nil
}

func ClientSelectMode() SelectMode {
	mode := SelectMode(clientSelectMode.Load())
	if mode < Primary || mode > Random {
		return Primary
	}
	return mode
}
