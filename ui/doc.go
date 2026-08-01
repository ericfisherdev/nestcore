// Package ui is the Hearth shell UI kit shared by every Hearth app (NSTR-128).
// It ships the structure both apps agree on — the 264px sidebar-plus-main
// geometry, Hanken Grotesk/Space Mono, the templ shell components, and the
// self-hosted HTMX/Alpine/font assets — while deliberately not shipping a
// palette. Nestova (warm sand/sage) and Nestorage (indigo-slate) are meant to
// look different; this package only guarantees they are built from the same
// bones.
//
// # The token-name contract
//
// The shell's CSS (ui/css/base.css and ui/css/theme.css) is written entirely
// against a fixed set of custom-property NAMES, prefixed "--hearth-", and
// contains no hex values of its own. Each app supplies the VALUES in its own
// palette stylesheet, declaring the same names:
//
//	:root {
//	  --hearth-surface: #faf7f0;       /* Nestova's warm sand */
//	  --hearth-surface: #f5f7fa;       /* Nestorage's indigo-slate, in its own file */
//	  ...
//	}
//
// ui/css/theme.css is the Tailwind v4 "@theme" snippet that maps Tailwind
// utilities (bg-surface, text-text, ...) onto var(--hearth-*) reads. An app's
// Tailwind entry point imports theme.css ahead of its own values file:
//
//	@import "tailwindcss" source(none);
//	@import "<path-to-nestcore>/ui/css/theme.css";
//	@import "./palette.css"; /* this app's own --hearth-* values */
//
// The full name list — surfaces, lines, text, the brand primary, the 5-slot
// member/owner color system (--hearth-slot-1..5-{solid,tint,fg}), radii,
// shadows, and the two font families — is documented at the top of
// ui/css/theme.css. Names are additive-only: token names and templ component
// signatures are shared API for two independently versioned binaries, so a
// consumer must never rename or remove a name or a prop, only add new ones
// and deprecate old ones in docs (mirrors the identity package's own
// additive-only rule, docs/testing.md's neighbor doc in nestova).
//
// # What is NOT in this package
//
// Palette values, PWA manifest/service-worker wiring, and app-specific motion
// (GSAP timelines) stay in each app — see ui/components's package doc for the
// exact seam (ShellProps.HeadExtras, SidebarExtra, SidebarFooter).
package ui
