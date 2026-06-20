package hybrid

import "strings"

// PorterStemmer implements the Porter stemming algorithm (Porter 1980), the opt-in
// English stemmer of spec 11 §9.3. It is deterministic and language-specific; it is
// off by default because stemming trades exact-match precision for recall. Enable it
// per index, and rebuild the index when changing it, since query-time and build-time
// stemming must agree.
type PorterStemmer struct{}

// Stem reduces an already-lowercased token to its Porter stem. Tokens of two letters
// or fewer are returned unchanged (the algorithm's measure is meaningless below that
// length).
func (PorterStemmer) Stem(w string) string {
	if len(w) <= 2 {
		return w
	}
	b := []byte(w)
	b = step1a(b)
	b = step1b(b)
	b = step1c(b)
	b = step2(b)
	b = step3(b)
	b = step4(b)
	b = step5a(b)
	b = step5b(b)
	return string(b)
}

// isConsonant reports whether b[i] is a consonant, with y consonant unless preceded
// by a consonant (Porter's definition).
func isConsonant(b []byte, i int) bool {
	switch b[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == 0 {
			return true
		}
		return !isConsonant(b, i-1)
	default:
		return true
	}
}

// measure counts the number of vowel-consonant sequences (the "m" of the algorithm)
// over b[:n].
func measure(b []byte, n int) int {
	m := 0
	i := 0
	for i < n && isConsonant(b, i) {
		i++
	}
	for i < n {
		for i < n && !isConsonant(b, i) {
			i++
		}
		if i >= n {
			break
		}
		m++
		for i < n && isConsonant(b, i) {
			i++
		}
	}
	return m
}

// hasVowel reports whether b[:n] contains a vowel.
func hasVowel(b []byte, n int) bool {
	for i := 0; i < n; i++ {
		if !isConsonant(b, i) {
			return true
		}
	}
	return false
}

// doubleConsonant reports whether b ends in a double consonant.
func doubleConsonant(b []byte) bool {
	n := len(b)
	if n < 2 {
		return false
	}
	return b[n-1] == b[n-2] && isConsonant(b, n-1)
}

// cvc reports whether b ends consonant-vowel-consonant with the final consonant not
// w, x, or y (the *o condition).
func cvc(b []byte) bool {
	n := len(b)
	if n < 3 {
		return false
	}
	if !isConsonant(b, n-3) || isConsonant(b, n-2) || !isConsonant(b, n-1) {
		return false
	}
	switch b[n-1] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

func ends(b []byte, suf string) bool { return strings.HasSuffix(string(b), suf) }

// replaceSuffix swaps a matched suffix for a replacement.
func replaceSuffix(b []byte, match, repl string) []byte {
	return append(b[:len(b)-len(match)], repl...)
}

func step1a(b []byte) []byte {
	switch {
	case ends(b, "sses"):
		return replaceSuffix(b, "sses", "ss")
	case ends(b, "ies"):
		return replaceSuffix(b, "ies", "i")
	case ends(b, "ss"):
		return b
	case ends(b, "s"):
		return b[:len(b)-1]
	}
	return b
}

func step1b(b []byte) []byte {
	switch {
	case ends(b, "eed"):
		if measure(b, len(b)-3) > 0 {
			return b[:len(b)-1]
		}
		return b
	case ends(b, "ed"):
		stem := b[:len(b)-2]
		if hasVowel(stem, len(stem)) {
			return step1bPost(stem)
		}
		return b
	case ends(b, "ing"):
		stem := b[:len(b)-3]
		if hasVowel(stem, len(stem)) {
			return step1bPost(stem)
		}
		return b
	}
	return b
}

// step1bPost applies the post-processing after an ed/ing removal left a stem.
func step1bPost(b []byte) []byte {
	switch {
	case ends(b, "at"), ends(b, "bl"), ends(b, "iz"):
		return append(b, 'e')
	case doubleConsonant(b):
		switch b[len(b)-1] {
		case 'l', 's', 'z':
			return b
		}
		return b[:len(b)-1]
	case measure(b, len(b)) == 1 && cvc(b):
		return append(b, 'e')
	}
	return b
}

func step1c(b []byte) []byte {
	if ends(b, "y") {
		stem := b[:len(b)-1]
		if hasVowel(stem, len(stem)) {
			b[len(b)-1] = 'i'
		}
	}
	return b
}

// step2pairs are the (suffix, replacement) rules of step 2, applied when m>0.
var step2pairs = [][2]string{
	{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"}, {"anci", "ance"},
	{"izer", "ize"}, {"bli", "ble"}, {"alli", "al"}, {"entli", "ent"},
	{"eli", "e"}, {"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"},
	{"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"}, {"fulness", "ful"},
	{"ousness", "ous"}, {"aliti", "al"}, {"iviti", "ive"}, {"biliti", "ble"},
	{"logi", "log"},
}

func step2(b []byte) []byte {
	for _, p := range step2pairs {
		if ends(b, p[0]) {
			stem := b[:len(b)-len(p[0])]
			if measure(stem, len(stem)) > 0 {
				return append(stem, p[1]...)
			}
			return b
		}
	}
	return b
}

var step3pairs = [][2]string{
	{"icate", "ic"}, {"ative", ""}, {"alize", "al"}, {"iciti", "ic"},
	{"ical", "ic"}, {"ful", ""}, {"ness", ""},
}

func step3(b []byte) []byte {
	for _, p := range step3pairs {
		if ends(b, p[0]) {
			stem := b[:len(b)-len(p[0])]
			if measure(stem, len(stem)) > 0 {
				return append(stem, p[1]...)
			}
			return b
		}
	}
	return b
}

var step4suffixes = []string{
	"al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement",
	"ment", "ent", "ou", "ism", "ate", "iti", "ous", "ive", "ize",
}

func step4(b []byte) []byte {
	// "ion" is special: only removed when preceded by s or t.
	if ends(b, "ion") {
		stem := b[:len(b)-3]
		if measure(stem, len(stem)) > 1 && len(stem) > 0 {
			last := stem[len(stem)-1]
			if last == 's' || last == 't' {
				return stem
			}
		}
		return b
	}
	for _, suf := range step4suffixes {
		if ends(b, suf) {
			stem := b[:len(b)-len(suf)]
			if measure(stem, len(stem)) > 1 {
				return stem
			}
			return b
		}
	}
	return b
}

func step5a(b []byte) []byte {
	if ends(b, "e") {
		stem := b[:len(b)-1]
		m := measure(stem, len(stem))
		if m > 1 || (m == 1 && !cvc(stem)) {
			return stem
		}
	}
	return b
}

func step5b(b []byte) []byte {
	if measure(b, len(b)) > 1 && doubleConsonant(b) && b[len(b)-1] == 'l' {
		return b[:len(b)-1]
	}
	return b
}
