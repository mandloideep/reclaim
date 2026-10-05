// Package units parses and formats byte sizes and ages for flags and output.
//
// Sizes use decimal SI units (1 kB = 1000 B) by default, matching Finder and
// "docker system df". Binary units such as MiB are accepted when parsing.
package units

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var sizeUnits = map[string]float64{
	"":    1,
	"b":   1,
	"k":   1e3,
	"kb":  1e3,
	"m":   1e6,
	"mb":  1e6,
	"g":   1e9,
	"gb":  1e9,
	"t":   1e12,
	"tb":  1e12,
	"kib": 1 << 10,
	"mib": 1 << 20,
	"gib": 1 << 30,
	"tib": 1 << 40,
}

// ParseSize parses a size such as "50MB", "1.5 GB", "512KiB" or "1000".
// A bare number is a byte count.
func ParseSize(s string) (int64, error) {
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, errors.New("empty size")
	}
	i := strings.IndexFunc(in, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' })
	num, unit := in, ""
	if i >= 0 {
		num, unit = strings.TrimSpace(in[:i]), strings.TrimSpace(in[i:])
	}
	mult, ok := sizeUnits[strings.ToLower(unit)]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit %q", s, unit)
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("size %q: invalid number", s)
	}
	bytes := v * mult
	if bytes > math.MaxInt64 {
		return 0, fmt.Errorf("size %q: too large", s)
	}
	return int64(bytes), nil
}

// FormatSize renders a byte count with one decimal and a decimal SI unit,
// such as "12.3 MB". Values below 1 kB are shown in bytes.
func FormatSize(n int64) string {
	if n < 0 {
		return "-" + FormatSize(-n)
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"kB", "MB", "GB", "TB", "PB", "EB"}
	v := float64(n)
	for _, u := range units {
		v /= 1000
		if v < 999.95 {
			return strconv.FormatFloat(v, 'f', 1, 64) + " " + u
		}
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " EB"
}

// ParseAge parses an age such as "90d", "12w", "1y", "36h" or any Go
// duration. A day is 24 hours, a week 7 days and a year 365 days.
func ParseAge(s string) (time.Duration, error) {
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, errors.New("empty age")
	}
	day := 24 * time.Hour
	suffixes := map[string]time.Duration{"d": day, "w": 7 * day, "y": 365 * day}
	for suf, mult := range suffixes {
		if numStr, ok := strings.CutSuffix(in, suf); ok {
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("age %q: invalid number", s)
			}
			return time.Duration(n * float64(mult)), nil
		}
	}
	d, err := time.ParseDuration(in)
	if err != nil {
		return 0, fmt.Errorf("age %q: want a number followed by d, w, y or a Go duration: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("age %q: must not be negative", s)
	}
	return d, nil
}

// FormatAge renders a duration as a short human age such as "3d", "5w" or "2y".
func FormatAge(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d < time.Hour:
		return "now"
	case d < day:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 14*day:
		return strconv.Itoa(int(d/day)) + "d"
	case d < 365*day:
		return strconv.Itoa(int(d/(7*day))) + "w"
	default:
		years := strconv.FormatFloat(float64(d)/float64(365*day), 'f', 1, 64)
		return strings.TrimSuffix(years, ".0") + "y"
	}
}
