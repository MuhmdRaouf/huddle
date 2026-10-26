package jsscalar

import (
	"slices"
	"testing"
)

// localeGolden holds the strings radar's tie-breaks sort: model ids, tool names, mixed case,
// digits, dashes, dots, underscores, accented Latin, a ligature and one decomposed string.
// localeBunOrder and localeBunSigns are bun 1.4.2's answers (ICU 78.1, whose default locale
// here is Intl.DateTimeFormat().resolvedOptions().locale === "en-US").
// LocaleCompare must return the same sign for every pair.
var localeGolden = []string{
	"claude-sonnet-4-5",
	"claude-opus-4-6",
	"claude-haiku-4-5",
	"claude-3-7-sonnet",
	"glm-4.6",
	"glm-4.5",
	"glm-4.5-air",
	"GLM-4.6",
	"kimi-k2-0905",
	"kimi-k2-thinking",
	"deepseek-chat",
	"deepseek-reasoner",
	"MiniMax-M2",
	"minimax-m2",
	"qwen3-max",
	"qwen-max",
	"qwen3-max-preview",
	"Bash",
	"Read",
	"Edit",
	"Write",
	"Grep",
	"Glob",
	"WebFetch",
	"WebSearch",
	"TodoWrite",
	"Task",
	"Agent",
	"NotebookEdit",
	"LSP",
	"Skill",
	"AskUserQuestion",
	"ExitPlanMode",
	"SlashCommand",
	"ListMcpResourcesTool",
	"ReadMcpResourceTool",
	"mcp__4_5v_mcp__analyze_image",
	"mcp__web_reader__webReader",
	"bash",
	"read",
	"webFetch",
	"todoWrite",
	"a",
	"A",
	"b",
	"B",
	"aa",
	"ab",
	"a-b",
	"a_b",
	"a.b",
	"a b",
	"_lib",
	"-lib",
	".lib",
	"lib2",
	"lib10",
	"lib",
	"Lib",
	"Ärger",
	"äther",
	"Éclair",
	"eclair",
	"naïve",
	"naive",
	"café",
	"cafe",
	"cafe\u0301",
	"æon",
	"Aeon",
	"zebra",
	"Zebra",
	"Über",
	"uber",
	"ø",
	"Ø",
	"ß",
	"SS",
	"ss",
}

// localeBunOrder is bun's localeCompare sort of localeGolden.
var localeBunOrder = []string{
	"_lib",
	"-lib",
	".lib",
	"a",
	"A",
	"a b",
	"a_b",
	"a-b",
	"a.b",
	"aa",
	"ab",
	"Aeon",
	"æon",
	"Agent",
	"Ärger",
	"AskUserQuestion",
	"äther",
	"b",
	"B",
	"bash",
	"Bash",
	"cafe",
	"café",
	"cafe\u0301",
	"claude-3-7-sonnet",
	"claude-haiku-4-5",
	"claude-opus-4-6",
	"claude-sonnet-4-5",
	"deepseek-chat",
	"deepseek-reasoner",
	"eclair",
	"Éclair",
	"Edit",
	"ExitPlanMode",
	"glm-4.5",
	"glm-4.5-air",
	"glm-4.6",
	"GLM-4.6",
	"Glob",
	"Grep",
	"kimi-k2-0905",
	"kimi-k2-thinking",
	"lib",
	"Lib",
	"lib10",
	"lib2",
	"ListMcpResourcesTool",
	"LSP",
	"mcp__4_5v_mcp__analyze_image",
	"mcp__web_reader__webReader",
	"minimax-m2",
	"MiniMax-M2",
	"naive",
	"naïve",
	"NotebookEdit",
	"ø",
	"Ø",
	"qwen-max",
	"qwen3-max",
	"qwen3-max-preview",
	"read",
	"Read",
	"ReadMcpResourceTool",
	"Skill",
	"SlashCommand",
	"ss",
	"SS",
	"ß",
	"Task",
	"todoWrite",
	"TodoWrite",
	"uber",
	"Über",
	"webFetch",
	"WebFetch",
	"WebSearch",
	"Write",
	"zebra",
	"Zebra",
}

// localeBunSigns is bun's verdict for every pair (i, j), i < j, of localeGolden in input
// order, flattened: "<" when golden[i] sorts first, ">" when golden[j] does, "=" on a tie.
const localeBunSigns = "" +
	">>><<<<<<<<<<<<<><<<<<<<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<>>>>><<<<<<<<<>><<<<<<<<<<<<<><<<<<<" +
	"<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<>>>>><<<<<<<<<><<<<<<<<<<<<<><<<<<<<<<><<<><<<<<<><<<>>>>>>" +
	">>>>>>><<<<>><<<<>>>>><<<<<<<<<<<<<<<<<<<<<<><<<<<<<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<>>>>><<<" +
	"<<<<<<>><<<>><<<<<><><<<<<<<><<<>><<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<<<<>><<<<<><><<<<<<" +
	"<><<<>><<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<<<>><<<<<><><<<<<<<><<<>><<<<<><<<>>>>>>>>>>>>" +
	"><<<<>>>><<>>>>><<<<<<<<<<<>><<<<<><><<<<<<<><<<>><<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<>><" +
	"<<<<><><>><<<<><<<>><<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<>><<<<<><><>><<<<><<<>><<<<<><<<>>" +
	">>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<<<<<<><<<<<<<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<>>>>><<<<<<" +
	"<<<<<<<<><<<<<<<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<>>>>><<<<<<<<<><<<><><>><<<<><><>><><>>><<<>" +
	">>>>>>>>>>>>>>>>>>>><<>>>>><<<<<<<<<<<<><><>><<<<><><>><><>>><<<>>>>>>>>>>>>>>>>>>>>><<>>>>><<<<<<<<" +
	"<><><><>><<<<>>><>><><>>><<<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<<><><>><<<<>>><>><><>>><<<>>>>>>>>>" +
	">>>>>>>>>>>>>>>>>>><<<<>><<<><><>><<<<>>><>><><>>><<<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<<<<<<<<<<>" +
	"<<<><<<<<<><<<>>>>>>>>>>>>><<<<>><<<<<<<>><<<<<<<<<><>><<<<>>><>><><>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>" +
	">><<<<>><<<<<<<<<<><<<><<<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>" +
	">>>>>>>>>>>>>>>>>>><<>>>>>>>><<<<><<<>><<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<<<<><<<>><<<<<" +
	"><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<<>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>><<>>>>>>>>>>>" +
	">>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>><<>>>>>>>>>>>>>>>>>>>>><>>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>>" +
	">>>>>>>>>>>>>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>>>>><<<<<<<<<<<<<<>><<>>>>>>>>><<<<<<<<<<<<<>><<<" +
	"<<<<<<><>><><>>><<<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<<<<<<<>><><<<><<<>>>>>>>>>>>>>>>>>>>>><<>>>>><<<<" +
	"<<<<<>><>>>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<<<<<<<<<<<>><<>>>>>>>>><<<<><<<<<<<<>><<<<<<<<<" +
	"<<<<<><<<>>>>>>>>>>>>><<<<>>>><<>>>>><<<<<<<<<>>>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<<<<><<<>>" +
	">>>>>>>>>>>>>>>>>>><<>>>>><<<<<<<<<>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<<><<<>>>>>>>>>>>>>>>>>" +
	">>>><<>>>>><<<<<<<<<><<<>>>>>>>>>>>>>>>>>>>>><<>>>>><<<<<<<<<<<<>>>>>>>>>>>>><<<<>><<<<<<<>><<<<<<<<" +
	"<<<>>>>>>>>>>>>>>>>>>>>>>>>>>>><<<<>><<<>>>>>>>>>>>>>>>>>>>>>>>>>>>>><<>>>>>>>>>>>>>>>>>>>>>>>>>>>>>" +
	">>>>>><<<<>>>>><<<<<<<<<>>><<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<>>><<<<<<<<<<<<<<<<<<<<<<<<<>>>>>>>>><<<<" +
	">><<<<<<<>><<<<<<<<<>>>>>>>>><<<<>><<<<<<<>><<<<<<<<<<>>>>>>><<<<<<<<<<<<<<<<<<<<<<<<>>>>>>><<<<<<<<" +
	"<<<<<<<<<<<<<<<<><>>>><<<<<<<<<<<<<<<<<<<<<<<<<>>>><<<<<<<<<<<<<<<<<<<<<<<<>>>><<<<<<<<<<<<<<<<<<<<<" +
	"<<<>>><<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<" +
	"<<<<<>>>>>>><<>>>>><<<<<<<<<>>>>>><<>>>>><<<<<<<<<<>>>><<>>>>><<<<<<<<<>>>><<>>>>><<<<<<<<<<<<<<<<<>" +
	"><<<<<<<<<<<<<<<<>><<<<<<<<<><<>>>>><<<<<<<<<<<>>>>><<<<<<<<<>>>>>><<<<<<<<<>>>>><<<<<<<<<>=>><<<<<<" +
	"<<<<>><<<<<<<<<>><<<<<<<<<><<<<<<<<<<<<<<<<<<<>>>>>>>>>>>>>>>>>>>>>>>>><<<<<<<>>>"

func TestLocaleCompareSignsMatchBun(t *testing.T) {
	at := 0
	for i := 0; i < len(localeGolden); i++ {
		for j := i + 1; j < len(localeGolden); j++ {
			got := LocaleCompare(localeGolden[i], localeGolden[j])
			want := map[byte]int{'<': -1, '=': 0, '>': 1}[localeBunSigns[at]]
			if (got < 0) != (want < 0) || (got > 0) != (want > 0) {
				t.Errorf("LocaleCompare(%q, %q) = %d, want sign %q",
					localeGolden[i], localeGolden[j], got, string(localeBunSigns[at]))
			}
			at++
		}
	}
	if at != len(localeBunSigns) {
		t.Fatalf("checked %d pairs, the golden holds %d", at, len(localeBunSigns))
	}
}

func TestLocaleCompareSortsLikeBun(t *testing.T) {
	sorted := slices.Clone(localeGolden)
	slices.SortStableFunc(sorted, LocaleCompare)
	for i, want := range localeBunOrder {
		if sorted[i] != want {
			t.Fatalf("sorted[%d] = %q, want %q (bun's order)", i, sorted[i], want)
		}
	}
}

// TestLocaleCompareAccentsAndFallbacks covers the accent ranks the golden only samples and
// the fallback order the doc comment promises: decomposed marks equal their precomposed kin
// and stack in sequence, the scripts the tables skip compare after Latin by code point, and
// an unnamed combining mark weighs behind every named accent. The first group is bun's
// answer too; the fallback group is this package's own rule.
func TestLocaleCompareAccentsAndFallbacks(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"á", "à", -1}, // acute before grave, decomposed like precomposed
		{"a̧", "á", 1},  // cedilla behind acute
		{"ä", "ä́", -1},
		{"à́", "á", 1},
		{"ś", "ś", 0}, // a decomposed mark folds into its letter
		{"ö", "ö", 0},
		{"z", "α", -1}, // uncovered scripts: after Latin, by code point
		{"Z", "α", -1},
		{"α", "β", -1},
		{"a b", "a b", 1},  // a no-break space weighs more than a plain one
		{"a b", "a_b", -1}, // …but less than punctuation
		{"á", "a͠", -1},   // an unnamed mark weighs behind every named accent
	}
	for _, test := range tests {
		if got := LocaleCompare(test.a, test.b); got != test.want {
			t.Errorf("LocaleCompare(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
		if got := LocaleCompare(test.b, test.a); got != -test.want {
			t.Errorf("LocaleCompare(%q, %q) = %d, want %d", test.b, test.a, got, -test.want)
		}
	}
}
