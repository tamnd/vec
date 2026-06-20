package storage

import "strconv"

// formatFloat renders a float64 distinct-count key with full round-trip precision.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
