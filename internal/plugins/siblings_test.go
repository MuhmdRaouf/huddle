package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The five provider plugins are hand-written copies of one another by design (no generator, each
// reads on its own). This keeps them copies: every file of every sibling is compared, file by
// file, with zai's after both are normalised — the provider's name, NAME, display name, brand
// words, model ids and labels replaced by placeholders, also in paths, and markdown prose compared
// as words so a paragraph's wrapping is no difference. A difference passes only when allowed
// names it with its reason; anything else fails with the first lines that differ.

const siblingReference = "zai"

var siblings = []string{"kimi", "deepseek", "minimax", "qwen"}

// brandWords is how the prose names each provider besides its display name: the model family and
// the vendor who issues the key. All of these, the display name and the capitalised name
// normalise to one <Brand> placeholder.
var brandWords = map[string][]string{
	"zai":      {"GLM", "Z.ai"},
	"kimi":     {"Kimi", "Moonshot AI"},
	"deepseek": {"DeepSeek"},
	"minimax":  {"MiniMax"},
	"qwen":     {"Qwen", "Alibaba Cloud"},
}

// allowedDiffers are the differences that may stand, per canonical path: every entry says why.
// `*` covers all four siblings; a provider's name only that one.
var allowedDiffers = map[string]map[string]string{
	"README.md": {
		"*": "vendor prose (models, prices, regions); reviewed in the docs pass",
	},
	".claude-plugin/plugin.json": {
		"*": "the vendor's name in the description",
	},
	"agents/<flash-id>.md": {
		"minimax": "the owner's call: highspeed is the fast tier at about twice M3's price, so its description may not say cheaper",
	},
}

// replacement is one normalisation step: every `from` becomes `to`.
type replacement struct {
	from string
	to   string
}

// namesOf is one provider's names, longest first: the model ids, labels and prefixes its catalog
// names, then its display name, brand words, NAME and bare name.
func namesOf(provider domain.Provider) []replacement {
	main := provider.Catalog[domain.ModelTierMain]
	flash := provider.Catalog[domain.ModelTierFlash]
	var pairs []replacement
	for _, prefix := range provider.Router.ModelPrefixes {
		pairs = append(pairs, replacement{from: "`" + prefix + "*`", to: "`<prefix>*`"})
	}
	pairs = append(pairs,
		replacement{main.Label, "<MainLabel>"},
		replacement{flash.Label, "<FlashLabel>"},
		replacement{main.ID, "<main-id>"},
		replacement{flash.ID, "<flash-id>"},
	)
	for _, ref := range []domain.ModelRef{main, flash} {
		if ref.BehavesAs != nil {
			pairs = append(pairs, replacement{from: `, behavesAs: "` + *ref.BehavesAs + `"`, to: ""})
		}
	}
	pairs = append(pairs, replacement{provider.Display, "<Brand>"})
	for _, word := range brandWords[provider.Name] {
		pairs = append(pairs, replacement{word, "<Brand>"})
	}
	pairs = append(pairs,
		replacement{strings.ToUpper(provider.Name), "<NAME>"},
		replacement{strings.ToUpper(provider.Name[:1]) + provider.Name[1:], "<Brand>"},
		replacement{provider.Name, "<name>"},
	)
	// Longest first, so a longer name is never half-replaced by a shorter one inside it. The sort
	// is stable: equal lengths keep the catalog's order.
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) })
	return pairs
}

// dewrap joins every single newline (and its surrounding horizontal space) into one space, the
// way the drift guard compares markdown prose as words. A blank line's newline stays.
func dewrap(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j < len(text) && text[j] == '\n' && (j+1 >= len(text) || text[j+1] != '\n') {
			j++
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			out.WriteByte(' ')
			i = j
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

// normalise applies one side's names to a file's text (or its path).
func normalise(text string, names []replacement, path string) string {
	if strings.HasSuffix(path, ".md") {
		text = dewrap(text)
	}
	for _, pair := range names {
		text = strings.ReplaceAll(text, pair.from, pair.to)
	}
	return text
}

// pluginFiles walks one plugin directory from disk and returns its files by normalised path.
func pluginFiles(t *testing.T, name string, names []replacement) map[string]string {
	t.Helper()
	root := pluginDirOf(name)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		files[normalise(relative, names, relative)] = relative
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func TestSiblingsMatchZai(t *testing.T) {
	referenceNames := namesOf(shippedProvider(t, siblingReference))
	referenceFiles := pluginFiles(t, siblingReference, referenceNames)
	var failures []string
	for _, sibling := range siblings {
		names := namesOf(shippedProvider(t, sibling))
		files := pluginFiles(t, sibling, names)
		for canonical, path := range referenceFiles {
			if _, together := files[canonical]; together {
				continue
			}
			failures = append(failures, sibling+": missing "+canonical+" ("+siblingReference+" has "+path+")")
		}
		for canonical, path := range files {
			reference, together := referenceFiles[canonical]
			if !together {
				failures = append(failures, sibling+": "+path+" has no counterpart in "+siblingReference)
				continue
			}
			ours := normalise(readFile(t, filepath.Join(pluginDirOf(sibling), path)), names, path)
			theirs := normalise(readFile(t, filepath.Join(pluginDirOf(siblingReference), reference)), referenceNames, reference)
			if ours == theirs {
				continue
			}
			if rule, allowed := allowedDiffers[canonical]; allowed && (rule["*"] != "" || rule[sibling] != "") {
				continue
			}
			failures = append(failures, sibling+": "+path+" differs from "+siblingReference+"'s "+
				reference+" after normalising names, "+firstDifference(theirs, ours))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("check-siblings: %d unexplained difference(s) between the provider plugins:\n- %s\n"+
			"Make the copies agree, or name the difference in allowedDiffers with its reason.",
			len(failures), strings.Join(failures, "\n- "))
	}
}

// shippedProvider is one shipped provider's data.
func shippedProvider(t *testing.T, name string) domain.Provider {
	t.Helper()
	provider, ok := providers.ByName(name)
	if !ok {
		t.Fatalf("providers has no %s", name)
	}
	return provider
}

// firstDifference names the first line where the reference and the sibling diverge.
func firstDifference(reference, sibling string) string {
	left := strings.Split(reference, "\n")
	right := strings.Split(sibling, "\n")
	for i := 0; i < max(len(left), len(right)); i++ {
		left_line, right_line := "<end>", "<end>"
		if i < len(left) {
			left_line = left[i]
		}
		if i < len(right) {
			right_line = right[i]
		}
		if left_line != right_line {
			return "line " + strconv.Itoa(i+1) + ":\n  " + siblingReference + ": " + left_line + "\n  sibling: " + right_line
		}
	}
	return "identical"
}

// readFile is one file's text.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
