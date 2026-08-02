package ui_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestcore/ui/static"
)

// hexColor matches a CSS hex color literal (#abc, #aabbcc, #aabbccdd). The
// token-name contract requires base.css and theme.css to declare zero hex
// values of their own — every color comes from a var(--hearth-*) read that
// each app's own palette file resolves.
var hexColor = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)

func TestThemeAndBaseCSS_DeclareNoHexValues(t *testing.T) {
	for _, path := range []string{"css/theme.css", "css/base.css"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if m := hexColor.FindString(string(content)); m != "" {
			t.Errorf("%s declares hex value %q; token values belong in each app's own palette file", path, m)
		}
	}
}

// TestDocImportSnippets_IncludeBaseCSS guards against the two copy-pasteable
// Tailwind entry-point snippets (package ui's doc and theme.css's own doc
// comment) drifting apart on whether base.css is imported — an app that
// follows a snippet missing it ships without the shell's @font-face,
// [x-cloak], and cursor rules.
func TestDocImportSnippets_IncludeBaseCSS(t *testing.T) {
	// The exact import line, not just any mention of the path — doc.go's
	// own prose ("The shell's CSS (ui/css/base.css and ui/css/theme.css)
	// is written entirely...") also contains the substring "ui/css/base.css",
	// so a looser check would pass even if the import line itself were removed.
	want := `@import "<path-to-nestcore>/ui/css/base.css";`
	for _, path := range []string{"doc.go", "css/theme.css"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(content), want) {
			t.Errorf("%s's import snippet doesn't contain %q", path, want)
		}
	}
}

func TestBaseCSS_FontURLsMatchStaticMountPath(t *testing.T) {
	content, err := os.ReadFile("css/base.css")
	if err != nil {
		t.Fatalf("reading css/base.css: %v", err)
	}
	for _, font := range []string{"hanken-grotesk.woff2", "space-mono.woff2"} {
		want := `url("` + static.MountPath + "fonts/" + font + `"`
		if !strings.Contains(string(content), want) {
			t.Errorf("css/base.css missing %q (static.MountPath changed without updating the CSS?)", want)
		}
	}
}
