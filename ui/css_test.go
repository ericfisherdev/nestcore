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
