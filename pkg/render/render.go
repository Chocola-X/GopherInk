// Package render is the single funnel for turning stored content and comments
// into HTML. It intentionally does not expose the raw goldmark output to
// callers; every rendering path routes through htmlsan so raw <script>, event
// handlers and dangerous schemes cannot escape into the response.
package render

import (
	"bytes"
	stdhtml "html"
	"html/template"
	"regexp"
	"strings"

	"github.com/Chocola-X/GopherInk/pkg/htmlsan"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Trust levels control how strict the sanitizer is applied.
type Trust int

const (
	// TrustPublic is used for public-facing UGC (comments, guest submissions).
	TrustPublic Trust = iota
	// TrustAuthor is used for content authored by contributor/editor roles.
	// It still runs the sanitizer but relaxes rel="nofollow" enforcement.
	TrustAuthor
	// TrustAdmin is used for administrator-authored content.
	TrustAdmin
)

// PlainTextHTML returns a safe rendition of a plain-text block. Newlines are
// converted to paragraph breaks. The output is HTML-escaped up-front and then
// funnelled through the sanitizer to guarantee a defensible surface.
func PlainTextHTML(input string) template.HTML {
	input = strings.TrimPrefix(input, "<!--plaintext-->")
	escaped := stdhtml.EscapeString(input)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\n\n", "</p><p>")
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return template.HTML("<p>" + escaped + "</p>")
}

// MarkdownHTML converts Markdown to HTML and enforces the public UGC policy.
// This is the entrypoint used by comments and by content authored below the
// editor role.
func MarkdownHTML(input string) template.HTML {
	return MarkdownHTMLWithTrust(input, TrustPublic)
}

// MarkdownHTMLWithTrust converts Markdown and applies a sanitizer policy that
// matches the caller's trust level.
func MarkdownHTMLWithTrust(input string, trust Trust) template.HTML {
	input = strings.TrimPrefix(input, "<!--markdown-->")
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(input), &buf); err != nil {
		return PlainTextHTML(input)
	}
	return template.HTML(htmlsan.Sanitize(buf.String(), policyFor(trust)))
}

// HTMLBlock takes raw HTML authored by a trusted user and returns a sanitized
// version. It never returns the input verbatim, even for administrators.
func HTMLBlock(input string, trust Trust) template.HTML {
	return template.HTML(htmlsan.Sanitize(input, policyFor(trust)))
}

// ContentHTML dispatches to the appropriate renderer based on inline hints
// (<!--markdown-->, <!--plaintext-->) or the configured global mode.
func ContentHTML(input, mode string) template.HTML {
	return ContentHTMLWithTrust(input, mode, TrustPublic)
}

// ContentHTMLWithTrust is the trust-aware variant used by the CMS core.
func ContentHTMLWithTrust(input, mode string, trust Trust) template.HTML {
	if strings.HasPrefix(input, "<!--markdown-->") {
		return MarkdownHTMLWithTrust(input, trust)
	}
	if strings.HasPrefix(input, "<!--plaintext-->") {
		return PlainTextHTML(input)
	}
	switch mode {
	case "autop", "plaintext", "plain":
		return PlainTextHTML(input)
	case "html":
		return HTMLBlock(input, trust)
	default:
		return MarkdownHTMLWithTrust(input, trust)
	}
}

// SanitizeHTML is exported for callers that already have HTML bytes and only
// need the sanitizer step. It is used by the comment renderer when the site
// admin has enabled an HTML allowlist for comments.
func SanitizeHTML(input string, trust Trust) template.HTML {
	return template.HTML(htmlsan.Sanitize(input, policyFor(trust)))
}

func policyFor(trust Trust) htmlsan.Policy {
	switch trust {
	case TrustAuthor, TrustAdmin:
		return htmlsan.AdminPolicy()
	default:
		return htmlsan.UGCPolicy()
	}
}

// Excerpt returns a plain-text excerpt of the input. It strips both HTML tags
// and common Markdown syntax before applying the length limit so the result is
// safe for meta descriptions and RSS payloads.
func Excerpt(input string, n int) string {
	input = htmlsan.SanitizeToPlainText(input)
	input = stripMarkdown(input)
	text := strings.Join(strings.Fields(input), " ")
	if len([]rune(text)) <= n {
		return text
	}
	runes := []rune(text)
	return string(runes[:n]) + "..."
}

var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^#{1,6}\s+`),
	regexp.MustCompile("`{1,3}([^`]*)`{1,3}"),
	regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`),
	regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`),
	regexp.MustCompile(`[*_~>#-]+`),
}

func stripMarkdown(input string) string {
	input = strings.Split(input, "<!--more-->")[0]
	input = strings.TrimPrefix(input, "<!--markdown-->")
	input = strings.TrimPrefix(input, "<!--plaintext-->")
	for _, pattern := range markdownPatterns {
		input = pattern.ReplaceAllString(input, "$1")
	}
	return input
}
