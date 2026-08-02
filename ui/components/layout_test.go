package components_test

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestcore/ui/components"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func textComponent(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := w.Write([]byte(s))
		return err
	})
}

func TestLayout_AppNeutralHead(t *testing.T) {
	nav := []components.NavItem{{Label: "Bins", Href: "/bins", Active: true}}
	content := textComponent("<p>hello</p>")

	html := render(t, components.Layout(components.ShellProps{
		AppName:        "Nestorage",
		FaviconHref:    "/static/favicon.svg",
		ThemeColor:     "#3f5a80",
		StylesheetHref: "/static/css/app.css",
	}, nav, content))

	for _, want := range []string{
		"<title>Nestorage</title>",
		`href="/static/favicon.svg"`,
		`content="#3f5a80"`,
		`href="/static/css/app.css"`,
		`src="/hearth-static/js/htmx.min.js"`,
		`src="/hearth-static/js/alpine.min.js"`,
		"<p>hello</p>",
		">Bins<",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered layout missing %q\n---\n%s", want, html)
		}
	}
}

func TestLayout_AccessibilityAffordances(t *testing.T) {
	html := render(t, components.Layout(components.ShellProps{AppName: "Nestova"}, nil, textComponent("x")))

	for _, want := range []string{
		`href="#main-content"`,
		`id="main-content"`,
		`:inert="open"`,
		`aria-controls="sidebar"`,
		`id="sidebar"`,
		`aria-label="Primary"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered layout missing accessibility affordance %q\n---\n%s", want, html)
		}
	}
}

// TestLayout_ClosesDrawerOnResizeToDesktop guards against the drawer's
// open state surviving a narrow-to-desktop breakpoint cross (e.g. tablet
// rotation), which would otherwise leave <main :inert="open"> permanently
// inert once the sidebar snaps to its fixed desktop position.
func TestLayout_ClosesDrawerOnResizeToDesktop(t *testing.T) {
	html := render(t, components.Layout(components.ShellProps{AppName: "Nestova"}, nil, textComponent("x")))

	want := `@resize.window="if (open && window.innerWidth >= 768) open = false"`
	if !strings.Contains(html, want) {
		t.Errorf("rendered layout missing resize handler %q\n---\n%s", want, html)
	}
}

func TestLayout_OmitsAbsentOptionalProps(t *testing.T) {
	html := render(t, components.Layout(components.ShellProps{AppName: "Nestova"}, nil, textComponent("x")))

	for _, unwanted := range []string{`rel="icon"`, `name="theme-color"`, `rel="stylesheet"`} {
		if strings.Contains(html, unwanted) {
			t.Errorf("rendered layout has %q despite unset prop\n---\n%s", unwanted, html)
		}
	}
}

func TestLayout_SlotsRenderAppSpecificContent(t *testing.T) {
	html := render(t, components.Layout(components.ShellProps{
		AppName:       "Nestova",
		SidebarBrand:  textComponent(`<div id="brand">Nestova brand</div>`),
		SidebarExtra:  textComponent(`<div id="members">Family</div>`),
		SidebarFooter: textComponent(`<form id="logout"></form>`),
		HeadExtras:    textComponent(`<link rel="manifest" href="/static/manifest.webmanifest">`),
	}, nil, textComponent("content")))

	for _, want := range []string{
		`id="brand"`, "Nestova brand",
		`id="members"`, "Family",
		`id="logout"`,
		`rel="manifest"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered layout missing slot content %q\n---\n%s", want, html)
		}
	}
}

func TestLayout_SidebarBrandFallsBackToAppName(t *testing.T) {
	html := render(t, components.Layout(components.ShellProps{AppName: "Nestorage"}, nil, textComponent("x")))
	if !strings.Contains(html, `<span class="text-lg font-semibold">Nestorage</span>`) {
		t.Errorf("expected fallback brand label using AppName\n---\n%s", html)
	}
}

// TestLayout_NoExternalHosts is the automated check NSTR-128's acceptance
// criteria calls for: no page rendered through the shared shell may request
// an external host, because the appliance must render with the internet
// down. Both a Nestova-shaped and a Nestorage-shaped render are checked,
// since the slots let either app inject arbitrary head/sidebar content.
func TestLayout_NoExternalHosts(t *testing.T) {
	renders := []string{
		render(t, components.Layout(components.ShellProps{
			AppName:        "Nestova",
			FaviconHref:    "/static/favicon.svg",
			ThemeColor:     "#6f8c6a",
			StylesheetHref: "/static/css/app.css",
			HeadExtras:     textComponent(`<link rel="manifest" href="/static/manifest.webmanifest">`),
			SidebarFooter:  textComponent(`<form action="/logout"></form>`),
		}, []components.NavItem{{Label: "Home", Href: "/"}}, textComponent("<p>Nestova</p>"))),
		render(t, components.Layout(components.ShellProps{
			AppName:        "Nestorage",
			FaviconHref:    "/static/favicon.svg",
			ThemeColor:     "#3f5a80",
			StylesheetHref: "/static/css/app.css",
		}, []components.NavItem{{Label: "Bins", Href: "/bins"}}, textComponent("<p>Nestorage</p>"))),
	}

	for i, html := range renders {
		assertNoExternalHosts(t, html, i)
	}
}

// assertNoExternalHosts scans every href/src attribute value in html and
// fails if any resolves to an absolute URL naming a host — relative paths,
// fragments, and protocol-relative-free absolute paths are the only
// self-hosted-compatible forms.
func assertNoExternalHosts(t *testing.T, html string, caseIndex int) {
	t.Helper()
	for _, attr := range []string{`href="`, `src="`} {
		idx := 0
		for {
			start := strings.Index(html[idx:], attr)
			if start == -1 {
				break
			}
			start += idx + len(attr)
			end := strings.Index(html[start:], `"`)
			if end == -1 {
				t.Fatalf("case %d: unterminated %s attribute", caseIndex, attr)
			}
			value := html[start : start+end]
			idx = start + end

			if value == "" || strings.HasPrefix(value, "#") {
				continue
			}
			u, err := url.Parse(value)
			if err != nil {
				t.Fatalf("case %d: parsing %s=%q: %v", caseIndex, attr, value, err)
			}
			if u.Host != "" {
				t.Errorf("case %d: %s=%q names an external host %q", caseIndex, attr, value, u.Host)
			}
			if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
				t.Errorf("case %d: %s=%q uses disallowed scheme %q", caseIndex, attr, value, u.Scheme)
			}
		}
	}
}
