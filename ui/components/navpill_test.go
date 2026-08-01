package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestcore/ui/components"
)

func TestNavPill_Active(t *testing.T) {
	html := render(t, components.NavPill("Bins", "/bins", true))

	for _, want := range []string{
		`href="/bins"`,
		`aria-current="page"`,
		"bg-primary-tint",
		"text-primary-deep",
		">Bins<",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("active NavPill missing %q\n---\n%s", want, html)
		}
	}
}

func TestNavPill_Inactive(t *testing.T) {
	html := render(t, components.NavPill("Search", "/search", false))

	if strings.Contains(html, "aria-current") {
		t.Errorf("inactive NavPill should not carry aria-current\n---\n%s", html)
	}
	for _, want := range []string{"text-text-secondary", "hover:bg-sidebar-accent", ">Search<"} {
		if !strings.Contains(html, want) {
			t.Errorf("inactive NavPill missing %q\n---\n%s", want, html)
		}
	}
}

func TestLayout_NavRendersMixedActiveStates(t *testing.T) {
	nav := []components.NavItem{
		{Label: "All bins", Href: "/bins", Active: true},
		{Label: "Search", Href: "/search", Active: false},
		{Label: "Labels", Href: "/labels", Active: false},
	}
	html := render(t, components.Layout(components.ShellProps{AppName: "Nestorage"}, nav, textComponent("x")))

	for _, want := range []string{">All bins<", ">Search<", ">Labels<"} {
		if !strings.Contains(html, want) {
			t.Errorf("layout missing nav item %q\n---\n%s", want, html)
		}
	}
	if strings.Count(html, "aria-current=\"page\"") != 1 {
		t.Errorf("expected exactly one aria-current=\"page\", got html:\n%s", html)
	}
}
