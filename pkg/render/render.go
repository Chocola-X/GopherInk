package render

import (
	"bytes"
	stdhtml "html"
	"html/template"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

func PlainTextHTML(input string) template.HTML {
	input = strings.TrimPrefix(input, "<!--plaintext-->")
	escaped := stdhtml.EscapeString(input)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\n\n", "</p><p>")
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return template.HTML("<p>" + escaped + "</p>")
}

func MarkdownHTML(input string) template.HTML {
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
	return template.HTML(buf.String())
}

func ContentHTML(input, mode string) template.HTML {
	if strings.HasPrefix(input, "<!--markdown-->") {
		return MarkdownHTML(input)
	}
	if strings.HasPrefix(input, "<!--plaintext-->") {
		return PlainTextHTML(input)
	}
	switch mode {
	case "autop", "plaintext", "plain":
		return PlainTextHTML(input)
	case "html":
		return template.HTML(input)
	default:
		return MarkdownHTML(input)
	}
}

func Excerpt(input string, n int) string {
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
