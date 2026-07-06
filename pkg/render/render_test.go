package render

import (
	"strings"
	"testing"
)

func TestExcerptUsesMoreSeparator(t *testing.T) {
	got := Excerpt("intro text<!--more-->hidden text", 100)
	if got != "intro text" {
		t.Fatalf("excerpt = %q, want intro text", got)
	}
}

func TestMarkdownPrefixAndSafeHTML(t *testing.T) {
	html := string(ContentHTML("<!--markdown--># Title\n<script>alert(1)</script>", "autop"))
	if !strings.Contains(html, "<h1") {
		t.Fatalf("markdown prefix did not force markdown render: %s", html)
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("markdown render should escape raw html: %s", html)
	}
}
