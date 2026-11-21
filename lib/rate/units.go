package rate

import (
	"math"
	"strconv"
	"strings"
)

const (
	bitsPerByte      = 8
	bitsPerKilobit   = 1000
	bitsPerMegabit   = 1000 * 1000
	bytesPerKibibyte = 1024
)

// BpsFromKBPerSecond converts the historical KB/s storage unit to bps.
// NPS historically used 1024-byte kilobytes even though the label said KB/s.
func BpsFromKBPerSecond(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value > math.MaxInt64/(bytesPerKibibyte*bitsPerByte) {
		return math.MaxInt64
	}
	return value * bytesPerKibibyte * bitsPerByte
}

// KBPerSecondFromBps converts bps back to the historical KB/s unit. It is used
// only by the JSON export compatibility path. The result is rounded up so a
// positive ceiling never becomes the old layout's zero/unlimited value.
func KBPerSecondFromBps(value int64) int64 {
	if value <= 0 {
		return 0
	}
	const unit = bytesPerKibibyte * bitsPerByte
	converted := value / unit
	if value%unit != 0 {
		converted++
	}
	if converted < 1 {
		converted = 1
	}
	return converted
}

// BpsFromMbps converts a decimal Mbps value to bits per second.
func BpsFromMbps(value float64) int64 {
	if value <= 0 {
		return 0
	}
	if value > float64(math.MaxInt64)/bitsPerMegabit {
		return math.MaxInt64
	}
	return int64(math.Round(value * bitsPerMegabit))
}

// MbpsFromBps converts bps to decimal Mbps.
func MbpsFromBps(value int64) float64 {
	if value <= 0 {
		return 0
	}
	return float64(value) / bitsPerMegabit
}

// BytesPerSecondFromBps converts a network bit rate to the byte rate used by
// token buckets. The result is rounded down so a configured ceiling is never
// exceeded by the conversion itself.
func BytesPerSecondFromBps(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return value / bitsPerByte
}

// BpsFromBytesPerSecond converts a token-bucket byte rate back to bps.
func BpsFromBytesPerSecond(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value > math.MaxInt64/bitsPerByte {
		return math.MaxInt64
	}
	return value * bitsPerByte
}

// FormatMbps renders bps without unnecessary trailing zeroes.
func FormatMbps(value int64) string {
	if value <= 0 {
		return "0"
	}
	text := strconv.FormatFloat(MbpsFromBps(value), 'f', 3, 64)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if text == "" {
		return "0"
	}
	return text
}
