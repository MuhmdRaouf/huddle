package jsfmt

import (
	"strings"
	"unicode"
)

// LocaleCompare orders strings the way JavaScript's String.prototype.localeCompare does with
// no locale argument (the runtime's default ICU collation, tertiary strength): base letters
// and digits decide first, accents next — each in the position its letter sits at — case last
// with lowercase ahead of uppercase, and space, punctuation and symbols weighing less than
// digits, which weigh less than letters.
//
// The weights reproduce the ICU root collation for printable ASCII and the precomposed Latin
// letters (including the æ, œ, ß, þ and ĳ expansions). Anything else keeps a deterministic
// order: it compares after the Latin covered here, by code point.
func LocaleCompare(a, b string) int {
	ka, kb := collationKey(a), collationKey(b)
	if c := comparePrimaries(ka, kb); c != 0 {
		return c
	}
	if c := compareAccents(ka, kb); c != 0 {
		return c
	}
	return compareCases(ka, kb)
}

// collationElement is one base letter's weights: its primary (the letter, digit or mark
// itself), the accents piled on it and its case. A combining mark carries no primary of its
// own; it folds into the element before it.
type collationElement struct {
	primary  int32
	accents  []int32
	tertiary int32
}

// comparePrimaries walks both keys letter by letter; a missing letter (the shorter key) sorts
// first, the way "lib" orders before "lib10".
func comparePrimaries(ka, kb []collationElement) int {
	for i := range max(len(ka), len(kb)) {
		var wa, wb int32
		if i < len(ka) {
			wa = ka[i].primary
		}
		if i < len(kb) {
			wb = kb[i].primary
		}
		if wa != wb {
			return signOf(wa, wb)
		}
	}
	return 0
}

// compareAccents walks both keys in lockstep, comparing the accents at each letter: no accent
// sorts before one ("cafe" before "café"), and an accent counts where its letter sits, so
// "aäb" orders before "äab".
func compareAccents(ka, kb []collationElement) int {
	for i := range max(len(ka), len(kb)) {
		var wa, wb []int32
		if i < len(ka) {
			wa = ka[i].accents
		}
		if i < len(kb) {
			wb = kb[i].accents
		}
		if c := compareInts(wa, wb); c != 0 {
			return c
		}
	}
	return 0
}

// compareCases walks both keys' case weights: lowercase first, uppercase behind it, and the
// ligature weights (the second element of æ, ß and their kin) behind every plain case.
func compareCases(ka, kb []collationElement) int {
	for i := range max(len(ka), len(kb)) {
		var wa, wb int32
		if i < len(ka) {
			wa = ka[i].tertiary
		}
		if i < len(kb) {
			wb = kb[i].tertiary
		}
		if wa != wb {
			return signOf(wa, wb)
		}
	}
	return 0
}

// compareInts is the slice order: element by element, then the shorter first.
func compareInts(a, b []int32) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return signOf(a[i], b[i])
		}
	}
	return signOf(int32(len(a)), int32(len(b)))
}

// signOf says which weight is smaller.
func signOf(a, b int32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// collationKey turns a string into its collation elements, folding each combining mark into
// the element before it (a leading mark stands alone, primary-less).
func collationKey(s string) []collationElement {
	key := make([]collationElement, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			key = appendASCII(key, byte(r))
		default:
			key = appendRune(key, r)
		}
	}
	return key
}

// appendRune appends one non-ASCII rune's element: the expansions first, then the accented
// Latin table, then combining marks (folded right), then the code-point fallback.
func appendRune(key []collationElement, r rune) []collationElement {
	if expanded, ok := latinExpansion[r]; ok {
		return append(key, expanded...)
	}
	if accented, ok := latinAccent[r]; ok {
		return append(key, collationElement{
			primary:  letterPrimary(accented.base),
			accents:  []int32{accented.accent},
			tertiary: caseTertiary(r),
		})
	}
	if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
		rank := combiningRank(r)
		if len(key) > 0 {
			last := len(key) - 1
			key[last].accents = append(key[last].accents, rank)
			return key
		}
		return append(key, collationElement{accents: []int32{rank}})
	}
	if unicode.IsSpace(r) {
		return append(key, collationElement{primary: primaryWideSpace})
	}
	return append(key, collationElement{primary: primaryOther + r, tertiary: caseTertiary(r)})
}

// appendASCII appends one ASCII byte's element: punctuation and symbols rank in the ICU root
// order, digits above them, letters above those with case as the tertiary weight.
func appendASCII(key []collationElement, b byte) []collationElement {
	switch {
	case b == ' ' || b < 0x21 || b == 0x7F:
		return append(key, collationElement{primary: primarySpace})
	case b >= '0' && b <= '9':
		return append(key, collationElement{primary: primaryDigit0 + int32(b-'0')})
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return append(key, collationElement{
			primary:  letterPrimary(rune(b)),
			tertiary: caseTertiary(rune(b)),
		})
	default:
		return append(key, collationElement{primary: primaryPunct0 + int32(strings.IndexByte(punctOrder, b))})
	}
}

// letterPrimary is a letter's primary weight; upper- and lowercase share it.
func letterPrimary(r rune) int32 {
	if r >= 'A' && r <= 'Z' {
		r += 'a' - 'A'
	}
	return primaryLetterA + int32(r-'a')
}

// caseTertiary is the case weight: lowercase first, uppercase behind it.
func caseTertiary(r rune) int32 {
	if unicode.IsUpper(r) {
		return 1
	}
	return 0
}

// punctOrder lists the ASCII punctuation and symbols in the ICU root collation's primary
// order, all weighing less than digits; appendASCII hands out ranks by index here.
const punctOrder = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

// The primary ranks: space, the wider spaces (a no-break space weighs more than a plain
// one but less than punctuation), the punctuation above, digits, then letters. primaryAfterZ
// is the extra weight a dotless ı carries on top of its i. primaryOther starts the code-point
// fallback for the scripts and symbols the tables do not cover; secondaryOther does the same
// for combining marks the accent table does not name.
const (
	primarySpace     = 1
	primaryWideSpace = 2
	primaryPunct0    = 3
	primaryDigit0    = 35
	primaryLetterA   = 67
	primaryAfterZ    = 93
	primaryOther     = 1 << 20
	secondaryOther   = 1 << 20
)

// The case weights, in order: lowercase, then uppercase.
const (
	tertiaryLower = 0
	tertiaryUpper = 1
)

// lig is the accent every element of a ligature or the sharp s carries, ranked above every
// diacritic.
func lig() []int32 { return []int32{accentLigature} }

// The accent ranks in the ICU root collation's secondary order: acute before grave before
// breve and so on, a stroke (ø, đ) and the eth (ð) behind the diacritics.
const (
	accentAcute     = 1
	accentGrave     = 2
	accentBreve     = 3
	accentCircum    = 4
	accentCaron     = 5
	accentRing      = 6
	accentDiaeresis = 7
	accentDoubleAc  = 8
	accentTilde     = 9
	accentDot       = 10
	accentCedilla   = 11
	accentStroke    = 12
	accentOgonek    = 13
	accentMacron    = 14
	accentHook      = 15
	accentDotBelow  = 16
	accentEth       = 17
	accentLigature  = 40
)

// accentedRune is a precomposed Latin letter's decomposition: its base letter and accent rank.
type accentedRune struct {
	base   rune
	accent int32
}

// latinExpansion holds the letters that expand to more than one element: the ligatures and
// the sharp s, every element of which carries the ligature accent, a secondary weight above
// every diacritic, so "ae" orders before "åb" before "æ" and "ss" before "ß".
var latinExpansion = map[rune][]collationElement{
	'Æ': {{primaryLetterA, lig(), tertiaryUpper}, {primaryLetterE, lig(), tertiaryUpper}},
	'æ': {{primaryLetterA, lig(), tertiaryLower}, {primaryLetterE, lig(), tertiaryLower}},
	'Œ': {{primaryLetterO, lig(), tertiaryUpper}, {primaryLetterE, lig(), tertiaryUpper}},
	'œ': {{primaryLetterO, lig(), tertiaryLower}, {primaryLetterE, lig(), tertiaryLower}},
	'ß': {{primaryLetterS, lig(), tertiaryLower}, {primaryLetterS, lig(), tertiaryLower}},
	'ẞ': {{primaryLetterS, lig(), tertiaryUpper}, {primaryLetterS, lig(), tertiaryUpper}},
	'Þ': {{primaryLetterT, lig(), tertiaryUpper}, {primaryLetterH, lig(), tertiaryUpper}},
	'þ': {{primaryLetterT, lig(), tertiaryLower}, {primaryLetterH, lig(), tertiaryLower}},
	'Ĳ': {{primaryLetterI, lig(), tertiaryUpper}, {primaryLetterJ, lig(), tertiaryUpper}},
	'ĳ': {{primaryLetterI, lig(), tertiaryLower}, {primaryLetterJ, lig(), tertiaryLower}},
	'ı': {{primaryLetterI, nil, tertiaryLower}, {primaryAfterZ, nil, tertiaryLower}},
}

// letter rank constants for the expansion table above.
const (
	primaryLetterE = primaryLetterA + 'e' - 'a'
	primaryLetterH = primaryLetterA + 'h' - 'a'
	primaryLetterI = primaryLetterA + 'i' - 'a'
	primaryLetterJ = primaryLetterA + 'j' - 'a'
	primaryLetterO = primaryLetterA + 'o' - 'a'
	primaryLetterS = primaryLetterA + 's' - 'a'
	primaryLetterT = primaryLetterA + 't' - 'a'
)

// latinAccent decomposes the precomposed Latin-1 and Latin Extended-A letters the way ICU
// reads them: a base letter plus an accent that only weighs at the secondary level. The
// stroke letters (ø, đ, ł, ħ, ŧ) and the eth are accents on their base here too.
var latinAccent = map[rune]accentedRune{
	'À': {'a', accentGrave}, 'Á': {'a', accentAcute}, 'Â': {'a', accentCircum},
	'Ã': {'a', accentTilde}, 'Ä': {'a', accentDiaeresis}, 'Å': {'a', accentRing},
	'Ç': {'c', accentCedilla},
	'È': {'e', accentGrave}, 'É': {'e', accentAcute}, 'Ê': {'e', accentCircum},
	'Ë': {'e', accentDiaeresis},
	'Ì': {'i', accentGrave}, 'Í': {'i', accentAcute}, 'Î': {'i', accentCircum},
	'Ï': {'i', accentDiaeresis},
	'Ð': {'d', accentEth}, 'Ñ': {'n', accentTilde},
	'Ò': {'o', accentGrave}, 'Ó': {'o', accentAcute}, 'Ô': {'o', accentCircum},
	'Õ': {'o', accentTilde}, 'Ö': {'o', accentDiaeresis}, 'Ø': {'o', accentStroke},
	'Ù': {'u', accentGrave}, 'Ú': {'u', accentAcute}, 'Û': {'u', accentCircum},
	'Ü': {'u', accentDiaeresis}, 'Ý': {'y', accentAcute},
	'à': {'a', accentGrave}, 'á': {'a', accentAcute}, 'â': {'a', accentCircum},
	'ã': {'a', accentTilde}, 'ä': {'a', accentDiaeresis}, 'å': {'a', accentRing},
	'ç': {'c', accentCedilla},
	'è': {'e', accentGrave}, 'é': {'e', accentAcute}, 'ê': {'e', accentCircum},
	'ë': {'e', accentDiaeresis},
	'ì': {'i', accentGrave}, 'í': {'i', accentAcute}, 'î': {'i', accentCircum},
	'ï': {'i', accentDiaeresis},
	'ð': {'d', accentEth}, 'ñ': {'n', accentTilde},
	'ò': {'o', accentGrave}, 'ó': {'o', accentAcute}, 'ô': {'o', accentCircum},
	'õ': {'o', accentTilde}, 'ö': {'o', accentDiaeresis}, 'ø': {'o', accentStroke},
	'ù': {'u', accentGrave}, 'ú': {'u', accentAcute}, 'û': {'u', accentCircum},
	'ü': {'u', accentDiaeresis}, 'ý': {'y', accentAcute}, 'ÿ': {'y', accentDiaeresis},

	'Ā': {'a', accentMacron}, 'ā': {'a', accentMacron},
	'Ă': {'a', accentBreve}, 'ă': {'a', accentBreve},
	'Ą': {'a', accentOgonek}, 'ą': {'a', accentOgonek},
	'Ć': {'c', accentAcute}, 'ć': {'c', accentAcute},
	'Ĉ': {'c', accentCircum}, 'ĉ': {'c', accentCircum},
	'Ċ': {'c', accentDot}, 'ċ': {'c', accentDot},
	'Č': {'c', accentCaron}, 'č': {'c', accentCaron},
	'Ď': {'d', accentCaron}, 'ď': {'d', accentCaron},
	'Đ': {'d', accentStroke}, 'đ': {'d', accentStroke},
	'Ē': {'e', accentMacron}, 'ē': {'e', accentMacron},
	'Ė': {'e', accentDot}, 'ė': {'e', accentDot},
	'Ę': {'e', accentOgonek}, 'ę': {'e', accentOgonek},
	'Ě': {'e', accentCaron}, 'ě': {'e', accentCaron},
	'Ĝ': {'g', accentCircum}, 'ĝ': {'g', accentCircum},
	'Ğ': {'g', accentBreve}, 'ğ': {'g', accentBreve},
	'Ġ': {'g', accentDot}, 'ġ': {'g', accentDot},
	'Ģ': {'g', accentCedilla}, 'ģ': {'g', accentCedilla},
	'Ĥ': {'h', accentCircum}, 'ĥ': {'h', accentCircum},
	'Ħ': {'h', accentStroke}, 'ħ': {'h', accentStroke},
	'Ĩ': {'i', accentTilde}, 'ĩ': {'i', accentTilde},
	'Ī': {'i', accentMacron}, 'ī': {'i', accentMacron},
	'Ĭ': {'i', accentBreve}, 'ĭ': {'i', accentBreve},
	'Į': {'i', accentOgonek}, 'į': {'i', accentOgonek},
	'İ': {'i', accentDot},
	'Ĵ': {'j', accentCircum}, 'ĵ': {'j', accentCircum},
	'Ķ': {'k', accentCedilla}, 'ķ': {'k', accentCedilla},
	'Ĺ': {'l', accentAcute}, 'ĺ': {'l', accentAcute},
	'Ļ': {'l', accentCedilla}, 'ļ': {'l', accentCedilla},
	'Ľ': {'l', accentCaron}, 'ľ': {'l', accentCaron},
	'Ł': {'l', accentStroke}, 'ł': {'l', accentStroke},
	'Ń': {'n', accentAcute}, 'ń': {'n', accentAcute},
	'Ņ': {'n', accentCedilla}, 'ņ': {'n', accentCedilla},
	'Ň': {'n', accentCaron}, 'ň': {'n', accentCaron},
	'Ō': {'o', accentMacron}, 'ō': {'o', accentMacron},
	'Ŏ': {'o', accentBreve}, 'ŏ': {'o', accentBreve},
	'Ő': {'o', accentDoubleAc}, 'ő': {'o', accentDoubleAc},
	'Ŕ': {'r', accentAcute}, 'ŕ': {'r', accentAcute},
	'Ŗ': {'r', accentCedilla}, 'ŗ': {'r', accentCedilla},
	'Ř': {'r', accentCaron}, 'ř': {'r', accentCaron},
	'Ś': {'s', accentAcute}, 'ś': {'s', accentAcute},
	'Ŝ': {'s', accentCircum}, 'ŝ': {'s', accentCircum},
	'Ş': {'s', accentCedilla}, 'ş': {'s', accentCedilla},
	'Š': {'s', accentCaron}, 'š': {'s', accentCaron},
	'Ţ': {'t', accentCedilla}, 'ţ': {'t', accentCedilla},
	'Ť': {'t', accentCaron}, 'ť': {'t', accentCaron},
	'Ŧ': {'t', accentStroke}, 'ŧ': {'t', accentStroke},
	'Ũ': {'u', accentTilde}, 'ũ': {'u', accentTilde},
	'Ū': {'u', accentMacron}, 'ū': {'u', accentMacron},
	'Ŭ': {'u', accentBreve}, 'ŭ': {'u', accentBreve},
	'Ů': {'u', accentRing}, 'ů': {'u', accentRing},
	'Ű': {'u', accentDoubleAc}, 'ű': {'u', accentDoubleAc},
	'Ų': {'u', accentOgonek}, 'ų': {'u', accentOgonek},
	'Ŵ': {'w', accentCircum}, 'ŵ': {'w', accentCircum},
	'Ŷ': {'y', accentCircum}, 'ŷ': {'y', accentCircum},
	'Ź': {'z', accentAcute}, 'ź': {'z', accentAcute},
	'Ż': {'z', accentDot}, 'ż': {'z', accentDot},
	'Ž': {'z', accentCaron}, 'ž': {'z', accentCaron},
}

// combiningRank is a combining mark's accent rank; the marks the table does not name keep a
// code-point order of their own behind the named ones.
func combiningRank(r rune) int32 {
	switch r {
	case 0x0300:
		return accentGrave
	case 0x0301:
		return accentAcute
	case 0x0302:
		return accentCircum
	case 0x0303:
		return accentTilde
	case 0x0304:
		return accentMacron
	case 0x0306:
		return accentBreve
	case 0x0307:
		return accentDot
	case 0x0308:
		return accentDiaeresis
	case 0x0309:
		return accentHook
	case 0x030A:
		return accentRing
	case 0x030B:
		return accentDoubleAc
	case 0x030C:
		return accentCaron
	case 0x0323:
		return accentDotBelow
	case 0x0327:
		return accentCedilla
	case 0x0328:
		return accentOgonek
	default:
		return secondaryOther + r
	}
}
