package jsscalar

import (
	"regexp"
	"testing"
)

// TestSpaceClassMatchesJavaScripts walks every code point of the basic multilingual plane and
// checks SpaceClass and IsSpace against the answer JavaScript itself gives. The oracle is the
// list of code points /\s/ matches, generated with bun 1.4.2:
//
//	bun -e 'let m=[];for(let c=0;c<=0xffff;c++)if(/\s/.test(String.fromCodePoint(c)))m.push(c);let r=[];for(const c of m){const l=r[r.length-1];if(l&&l[1]===c-1)l[1]=c;else r.push([c,c])}console.log(JSON.stringify(r))'
//
// → [[9,13],[32,32],[160,160],[5760,5760],[8192,8202],[8232,8233],[8239,8239],[8287,8287],[12288,12288],[65279,65279]]
func TestSpaceClassMatchesJavaScripts(t *testing.T) {
	class := regexp.MustCompile(SpaceClass)
	for cp := 0; cp <= 0xFFFF; cp++ {
		// A lone surrogate cannot ride in a Go string, and /\s/ answers false for it anyway.
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		r := rune(cp)
		if got := class.MatchString(string(r)); got != bunSpace(r) {
			t.Errorf("SpaceClass matches U+%04X = %v, want %v", cp, got, !got)
		}
		if got := IsSpace(r); got != bunSpace(r) {
			t.Errorf("IsSpace(U+%04X) = %v, want %v", cp, got, !got)
		}
	}
}

// bunSpace is the recorded answer above, as ranges.
func bunSpace(r rune) bool {
	switch {
	case 0x0009 <= r && r <= 0x000D, r == 0x0020, r == 0x00A0, r == 0x1680,
		0x2000 <= r && r <= 0x200A, 0x2028 <= r && r <= 0x2029, r == 0x202F,
		r == 0x205F, r == 0x3000, r == 0xFEFF:
		return true
	}
	return false
}

// TestTrimIsStringPrototypeTrim holds String.prototype.trim's answers for the twenty strings
// below, recorded with bun 1.4.2:
//
//	bun -e 'const xs=["", "   ", "\t\n\v\f\r plain \v\f\n\t\r", "\u00a0nbsp around\u00a0",
//	  "\u3000ideographic around\u3000", "\ufeffbom around\ufeff", "\u0085NEL around\u0085",
//	  "\u2028ls\u2029", "\u200bzero-width\u200b", "\u2007figure space\u2007",
//	  "inner   spaces   kept", "\u00a0\u3000\ufeffmixed leading", "mixed trailing\u00a0\u3000\t",
//	  "no trim at all", "\u1680ogham\u1680", "\u202fnarrow nbsp\u202f", "\u205fmedium math\u205f",
//	  "emoji \u{1F600} inside \u{1F600} kept", "\u2000\u2001\u2002\u2003all en-quad family\u2003\u2002\u2001\u2000",
//	  "\u000bvertical only\u000b"]; console.log(JSON.stringify(xs.map(s=>[s,s.trim()])))'
func TestTrimIsStringPrototypeTrim(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"\t\n\v\f\r plain \v\f\n\t\r", "plain"},
		{"\u00a0nbsp around\u00a0", "nbsp around"},
		{"\u3000ideographic around\u3000", "ideographic around"},
		{"\ufeffbom around\ufeff", "bom around"},
		{"\u0085NEL around\u0085", "\u0085NEL around\u0085"}, // NEL is not JavaScript whitespace
		{"\u2028ls\u2029", "ls"},
		{"\u200bzero-width\u200b", "\u200bzero-width\u200b"}, // zero-width space is not whitespace
		{"\u2007figure space\u2007", "figure space"},
		{"inner   spaces   kept", "inner   spaces   kept"},
		{"\u00a0\u3000\ufeffmixed leading", "mixed leading"},
		{"mixed trailing\u00a0\u3000\t", "mixed trailing"},
		{"no trim at all", "no trim at all"},
		{"\u1680ogham\u1680", "ogham"},
		{"\u202fnarrow nbsp\u202f", "narrow nbsp"},
		{"\u205fmedium math\u205f", "medium math"},
		{"emoji \U0001F600 inside \U0001F600 kept", "emoji \U0001F600 inside \U0001F600 kept"},
		{"\u2000\u2001\u2002\u2003all en-quad family\u2003\u2002\u2001\u2000", "all en-quad family"},
		{"\u000bvertical only\u000b", "vertical only"},
	}
	for _, tt := range tests {
		if got := Trim(tt.in); got != tt.want {
			t.Errorf("Trim(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
