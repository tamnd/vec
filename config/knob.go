// Package config is the knob catalogue for vec (spec 22). It holds the single
// table of every configuration parameter: name, mutability tier, value kind,
// default, allowed range, and the documentation cross-reference. The vec package
// reads this table to validate PRAGMA and Option values, resolve effective
// settings, and render the configuration view. The package has no dependency on
// the engine, so the table can be tested on its own.
package config

import "strings"

// Tier is a knob's mutability tier (spec 22 §1.1). It decides where the value
// lives and whether it can change after the file is created.
type Tier int

const (
	// TierCreate is fixed at create time in the file header; changing it needs a
	// dump and reload.
	TierCreate Tier = iota
	// TierPersistent survives reopen; it lives in the catalog page.
	TierPersistent
	// TierSession lasts for one Open call or connection; it never touches disk.
	TierSession
	// TierServer is a server-process knob, set from config or env at startup.
	TierServer
	// TierProcess is process-wide and set once (GOMAXPROCS, log level/format).
	TierProcess
)

// String renders the tier as the short code used in the configuration view.
func (t Tier) String() string {
	switch t {
	case TierCreate:
		return "create"
	case TierPersistent:
		return "persistent"
	case TierSession:
		return "session"
	case TierServer:
		return "server"
	case TierProcess:
		return "process"
	default:
		return "unknown"
	}
}

// Kind is the value type of a knob, which decides how a raw string is parsed and
// range-checked.
type Kind int

const (
	// KindInt is a signed integer knob.
	KindInt Kind = iota
	// KindFloat is a floating-point knob.
	KindFloat
	// KindBool is an on/off knob.
	KindBool
	// KindEnum is one of a fixed set of string tokens.
	KindEnum
	// KindString is a free-form string (paths, addresses, URIs).
	KindString
	// KindFloatList is a bracketed list of floats, for fusion weights.
	KindFloatList
)

// String renders the kind for the configuration view.
func (k Kind) String() string {
	switch k {
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	case KindEnum:
		return "enum"
	case KindString:
		return "string"
	case KindFloatList:
		return "float[]"
	default:
		return "unknown"
	}
}

// Knob is one configuration parameter. The zero value is not useful; knobs are
// declared in the registry.
type Knob struct {
	// Name is the canonical knob name, as written in PRAGMA and config keys.
	Name string
	// Aliases are short forms that resolve to this knob (ef_search -> hnsw_ef_search).
	Aliases []string
	// Category groups the knob in the configuration view ("hnsw", "durability").
	Category string
	// Tier is the mutability tier.
	Tier Tier
	// Kind is the value type.
	Kind Kind
	// Default is the canonical string form of the compiled-in default.
	Default string
	// Computed is set when the default is derived from the environment at runtime
	// (adaptive cache, GOMAXPROCS, sqrt(N)); Default then holds a descriptive word.
	Computed bool
	// Enum lists the allowed tokens for a KindEnum knob, in canonical case.
	Enum []string
	// Min and Max bound a numeric knob when HasMin or HasMax is set.
	Min, Max       float64
	HasMin, HasMax bool
	// PowerOfTwo requires an integer knob to be a power of two (page_size).
	PowerOfTwo bool
	// ReadOnly marks a knob that can be read but never set through PRAGMA.
	ReadOnly bool
	// Doc is the short cross-reference string shown in the configuration view.
	Doc string
}

// IsEnum reports whether v is one of the knob's allowed enum tokens, matched
// case-insensitively. It returns the canonical-cased token on a match.
func (k *Knob) IsEnum(v string) (string, bool) {
	for _, e := range k.Enum {
		if strings.EqualFold(e, v) {
			return e, true
		}
	}
	return "", false
}
