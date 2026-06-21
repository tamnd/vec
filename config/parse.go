package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ValueError reports a raw value that a knob rejects. The vec package wraps it in
// the public ErrInvalidConfig type, carrying the knob name and the reason text.
type ValueError struct {
	Reason string
}

func (e *ValueError) Error() string { return e.Reason }

// Canonicalize parses raw against the knob's kind and range and returns the
// canonical string form stored in the catalog and returned by reads. A bad value
// returns a *ValueError describing the violation. The canonical form is stable:
// the same logical value always renders the same string, so a read after a set
// returns what the caller meant.
func (k *Knob) Canonicalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch k.Kind {
	case KindBool:
		return canonBool(raw)
	case KindEnum:
		if v, ok := k.IsEnum(raw); ok {
			return v, nil
		}
		return "", &ValueError{Reason: fmt.Sprintf("value %q is not one of %s", raw, strings.Join(k.Enum, ", "))}
	case KindInt:
		return k.canonInt(raw)
	case KindFloat:
		return k.canonFloat(raw)
	case KindFloatList:
		return canonFloatList(raw)
	case KindString:
		return raw, nil
	default:
		return "", &ValueError{Reason: "unknown knob kind"}
	}
}

func canonBool(raw string) (string, error) {
	switch strings.ToLower(raw) {
	case "on", "true", "1", "yes":
		return "on", nil
	case "off", "false", "0", "no":
		return "off", nil
	default:
		return "", &ValueError{Reason: fmt.Sprintf("value %q is not a boolean (on/off)", raw)}
	}
}

func (k *Knob) canonInt(raw string) (string, error) {
	n, err := parseSize(raw)
	if err != nil {
		return "", &ValueError{Reason: fmt.Sprintf("value %q is not an integer", raw)}
	}
	f := float64(n)
	if k.HasMin && f < k.Min {
		return "", &ValueError{Reason: fmt.Sprintf("value %d is below the minimum %g", n, k.Min)}
	}
	if k.HasMax && f > k.Max {
		return "", &ValueError{Reason: fmt.Sprintf("value %d is above the maximum %g", n, k.Max)}
	}
	if k.PowerOfTwo && (n <= 0 || n&(n-1) != 0) {
		return "", &ValueError{Reason: fmt.Sprintf("value %d is not a power of two", n)}
	}
	return strconv.FormatInt(n, 10), nil
}

func (k *Knob) canonFloat(raw string) (string, error) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", &ValueError{Reason: fmt.Sprintf("value %q is not a number", raw)}
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", &ValueError{Reason: fmt.Sprintf("value %q is not finite", raw)}
	}
	if k.HasMin && f < k.Min {
		return "", &ValueError{Reason: fmt.Sprintf("value %g is below the minimum %g", f, k.Min)}
	}
	if k.HasMax && f > k.Max {
		return "", &ValueError{Reason: fmt.Sprintf("value %g is above the maximum %g", f, k.Max)}
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

func canonFloatList(raw string) (string, error) {
	inner := strings.TrimSpace(raw)
	inner = strings.TrimPrefix(inner, "[")
	inner = strings.TrimSuffix(inner, "]")
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return "", &ValueError{Reason: fmt.Sprintf("value %q is not a list of numbers", raw)}
		}
		if f < 0 {
			return "", &ValueError{Reason: fmt.Sprintf("weight %g is negative", f)}
		}
		out = append(out, strconv.FormatFloat(f, 'g', -1, 64))
	}
	if len(out) == 0 {
		return "", &ValueError{Reason: fmt.Sprintf("value %q has no weights", raw)}
	}
	return "[" + strings.Join(out, ",") + "]", nil
}

// parseSize parses a plain integer or a byte size with a binary suffix
// (512MiB, 4GiB, 64KiB). A negative integer is allowed because cache_size uses
// the SQLite convention where a negative value means bytes (spec 22 §3.2).
func parseSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "GIB"), strings.HasSuffix(upper, "G"):
		mult = 1 << 30
	case strings.HasSuffix(upper, "MIB"), strings.HasSuffix(upper, "M"):
		mult = 1 << 20
	case strings.HasSuffix(upper, "KIB"), strings.HasSuffix(upper, "K"):
		mult = 1 << 10
	case strings.HasSuffix(upper, "B"):
		mult = 1
	}
	if mult != 1 || strings.HasSuffix(upper, "B") {
		upper = strings.TrimRight(upper, "B")
		upper = strings.TrimRight(upper, "GIMK")
		f, err := strconv.ParseFloat(strings.TrimSpace(upper), 64)
		if err != nil {
			return 0, err
		}
		return int64(f * float64(mult)), nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// ParseSize exposes the size parser so the vec package can read byte-suffixed
// values from env variables and DSN query strings the same way PRAGMA does.
func ParseSize(raw string) (int64, error) { return parseSize(raw) }
