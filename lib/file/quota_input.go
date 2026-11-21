package file

import (
	"math"
	"strconv"
	"strings"

	"ehang.io/nps/lib/rate"
)

// ParseIntQuotaInput interprets a dashboard/API quota field using the
// three-state contract used by User:
//
//	blank -> nil  (unlimited)
//	0     -> 0    (explicitly not included)
//	N > 0 -> N    (cap)
//
// The bool result reports whether the caller submitted the field at all, so
// edit handlers can preserve the stored value when a form omits it.
func ParseIntQuotaInput(raw string) (value *int, submitted bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true, nil
	}
	parsed, parseErr := strconv.Atoi(raw)
	if parseErr != nil || parsed < 0 {
		return nil, true, ErrInvalidQuotaValue
	}
	return LimitInt(parsed), true, nil
}

// ParseIntQuotaAlias interprets the historical API aliases where a literal
// zero meant unlimited. An empty value and an omitted field retain the same
// compatibility meaning; non-zero values are still caps. The dashboard's
// three-state parser remains separate because an explicit zero there means
// that the plan does not include the resource.
func ParseIntQuotaAlias(raw string) (value *int, err error) {
	value, _, err = ParseIntQuotaInput(raw)
	if err != nil {
		return nil, err
	}
	if value != nil && *value == 0 {
		return nil, nil
	}
	return value, nil
}

// ParseInt64QuotaInput is the int64 variant used by the shared bandwidth
// budget, which is stored in bits per second.
func ParseInt64QuotaInput(raw string) (value *int64, submitted bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true, nil
	}
	parsed, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || parsed < 0 {
		return nil, true, ErrInvalidQuotaValue
	}
	return LimitInt64(parsed), true, nil
}

// ErrInvalidQuotaValue is shared by the HTTP and MCP layers so both reject
// negative or malformed quota values with the same actionable message.
var ErrInvalidQuotaValue = quotaInputError{}

type quotaInputError struct{}

func (quotaInputError) Error() string {
	return "配额必须是不小于 0 的整数；留空表示不限，0 表示套餐不包含"
}

// ParseMbpsQuotaInput accepts a dashboard bandwidth field expressed in
// decimal Mbps and returns the persisted bps value. Blank means unlimited;
// zero means the plan does not include bandwidth. Fractional Mbps is allowed
// so a 10.5 Mbps plan does not have to be rounded before storage.
func ParseMbpsQuotaInput(raw string) (value *int64, submitted bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true, nil
	}
	mbps, parseErr := strconv.ParseFloat(raw, 64)
	if parseErr != nil || math.IsNaN(mbps) || math.IsInf(mbps, 0) || mbps < 0 {
		return nil, true, ErrInvalidMbpsValue
	}
	bps := rate.BpsFromMbps(mbps)
	if bps < 0 {
		return nil, true, ErrInvalidMbpsValue
	}
	return LimitInt64(bps), true, nil
}

// ParseLegacyMbpsAlias preserves the pre-three-state API convention where zero
// meant unlimited. Positive decimal Mbps values are still converted to bps.
func ParseLegacyMbpsAlias(raw string) (value *int64, err error) {
	value, _, err = ParseMbpsQuotaInput(raw)
	if err != nil {
		return nil, err
	}
	if value != nil && *value == 0 {
		return nil, nil
	}
	return value, nil
}

// ErrInvalidMbpsValue is returned for malformed bandwidth fields.
var ErrInvalidMbpsValue = mbpsInputError{}

type mbpsInputError struct{}

func (mbpsInputError) Error() string {
	return "带宽必须是不小于 0 的数字，单位为 Mbps；留空表示不限，0 表示套餐不包含"
}
