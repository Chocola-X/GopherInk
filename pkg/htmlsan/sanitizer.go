// Package htmlsan is the single authoritative HTML sanitizer for GopherInk.
//
// The core CMS treats every string that ends up in an HTML template as
// untrusted until it has been through this package. The package intentionally
// has no external dependencies so it can be audited quickly and so it works
// consistently across renderer, comment pipeline, feed pipeline and preview.
//
// Design goals:
//
//   - Whitelist based: only known-safe tags survive; anything else is either
//     escaped or dropped.
//   - Attribute allowlist per tag: an <a> may keep href/title, an <img> may
//     keep src/alt/title/width/height; unknown attributes are stripped.
//   - Scheme allowlist for URL attributes: http, https, mailto, tel and
//     relative paths only. javascript:, data:, vbscript:, file:, gopher: are
//     rejected. This applies uniformly to href/src/action/formaction/xlink:href.
//   - Event handlers are always stripped (on* attributes, style="expression()").
//   - <script>, <style>, <iframe>, <object>, <embed>, <link>, <meta>, <base>
//     and SVG are dropped entirely for public UGC. Administrator content may
//     retain <iframe> only when the URL passes the scheme filter.
//   - Comments (<!-- ... -->) and processing instructions are stripped so a
//     preserved comment can't reintroduce mXSS in browsers that resurrect them.
//
// The sanitizer is a two-pass streaming tokenizer implemented without third
// party dependencies. It is deliberately conservative: when uncertain, it
// drops the tag and escapes its literal representation. This is preferable to
// preserving markup that might be interpreted differently in a downstream
// browser (mXSS).
package htmlsan

import (
	"regexp"
	"strings"
	"unicode"
)

// Policy describes which tags and attributes survive sanitization.
type Policy struct {
	// AllowedTags maps a lower-case tag name to the set of allowed attributes
	// on that tag. An empty attribute map means the tag is allowed but must
	// carry no attributes.
	AllowedTags map[string]map[string]struct{}
	// AllowedURLSchemes is applied to href/src/action/formaction attributes.
	AllowedURLSchemes map[string]struct{}
	// ForceNoFollow, when true, adds rel="nofollow noopener" to every <a> that
	// keeps an href.
	ForceNoFollow bool
	// ForceTargetBlank, when true, adds target="_blank" to every <a> that
	// keeps an http/https href.
	ForceTargetBlank bool
	// AllowIFrame permits <iframe> when the src passes the URL scheme filter.
	AllowIFrame bool
	// MaxDepth limits nested tag depth to guard against pathological inputs.
	MaxDepth int
}

var (
	// selfClosingTags lists HTML void elements that never have a closing tag.
	selfClosingTags = map[string]struct{}{
		"br": {}, "hr": {}, "img": {}, "wbr": {}, "col": {},
	}
	// urlAttributes carries values whose scheme must be validated.
	urlAttributes = map[string]struct{}{
		"href": {}, "src": {}, "action": {}, "formaction": {},
		"cite": {}, "poster": {}, "background": {},
	}
	// eventAttrPrefix is used to strip on* handlers regardless of allowlist.
	eventAttrPrefix = "on"

	// tagAttrPattern extracts key=value / key="value" / key='value' / bare key
	// from an attribute string. It intentionally accepts unquoted values that
	// stop at whitespace or > so we can copy conservatively; anything the
	// pattern misses gets dropped.
	tagAttrPattern = regexp.MustCompile(
		`([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`,
	)
)

// UGCPolicy returns the default policy used for public-facing user-generated
// content (comments, guest submissions, feed excerpts).
func UGCPolicy() Policy {
	return Policy{
		AllowedTags: map[string]map[string]struct{}{
			"a":          {"href": {}, "title": {}},
			"abbr":       {"title": {}},
			"b":          {},
			"blockquote": {"cite": {}},
			"br":         {},
			"cite":       {},
			"code":       {"class": {}},
			"del":        {},
			"em":         {},
			"i":          {},
			"img":        {"src": {}, "alt": {}, "title": {}, "width": {}, "height": {}},
			"li":         {},
			"mark":       {},
			"ol":         {"start": {}},
			"p":          {},
			"pre":        {"class": {}},
			"q":          {"cite": {}},
			"s":          {},
			"span":       {"class": {}},
			"strong":     {},
			"sub":        {},
			"sup":        {},
			"u":          {},
			"ul":         {},
			"h1":         {"id": {}}, "h2": {"id": {}}, "h3": {"id": {}},
			"h4": {"id": {}}, "h5": {"id": {}}, "h6": {"id": {}},
			"hr":    {},
			"table": {}, "thead": {}, "tbody": {}, "tfoot": {},
			"tr": {}, "th": {"align": {}, "colspan": {}, "rowspan": {}, "scope": {}},
			"td": {"align": {}, "colspan": {}, "rowspan": {}},
			"figure":     {},
			"figcaption": {},
			"details":    {"open": {}},
			"summary":    {},
		},
		AllowedURLSchemes: map[string]struct{}{
			"http": {}, "https": {}, "mailto": {}, "tel": {},
		},
		ForceNoFollow:    true,
		ForceTargetBlank: true,
		MaxDepth:         64,
	}
}

// AdminPolicy loosens UGCPolicy for administrator-authored content. Iframes
// are permitted when their src passes the URL scheme filter, but scripts are
// still forbidden.
func AdminPolicy() Policy {
	p := UGCPolicy()
	p.AllowedTags["iframe"] = map[string]struct{}{
		"src": {}, "width": {}, "height": {}, "title": {},
		"allow": {}, "loading": {}, "referrerpolicy": {},
	}
	p.AllowedTags["video"] = map[string]struct{}{
		"src": {}, "controls": {}, "poster": {}, "width": {}, "height": {}, "preload": {},
	}
	p.AllowedTags["audio"] = map[string]struct{}{
		"src": {}, "controls": {}, "preload": {},
	}
	p.AllowedTags["source"] = map[string]struct{}{
		"src": {}, "type": {}, "media": {},
	}
	p.AllowedTags["div"] = map[string]struct{}{"class": {}, "id": {}}
	p.AllowedTags["section"] = map[string]struct{}{"class": {}, "id": {}}
	p.AllowedTags["article"] = map[string]struct{}{"class": {}, "id": {}}
	p.AllowedTags["header"] = map[string]struct{}{"class": {}}
	p.AllowedTags["footer"] = map[string]struct{}{"class": {}}
	p.ForceNoFollow = false
	p.ForceTargetBlank = false
	p.AllowIFrame = true
	return p
}

// EmptyPolicy is a convenience for "never allow anything, escape everything".
func EmptyPolicy() Policy {
	return Policy{
		AllowedTags:       map[string]map[string]struct{}{},
		AllowedURLSchemes: map[string]struct{}{},
	}
}

// Sanitize applies policy to input and returns HTML that is safe to embed in a
// template. Unknown tags are dropped; text is HTML-escaped.
func Sanitize(input string, policy Policy) string {
	if policy.MaxDepth <= 0 {
		policy.MaxDepth = 64
	}
	var out strings.Builder
	i := 0
	depth := 0
	openStack := make([]string, 0, 16)
	for i < len(input) {
		lt := strings.IndexByte(input[i:], '<')
		if lt < 0 {
			out.WriteString(escapeText(input[i:]))
			break
		}
		out.WriteString(escapeText(input[i : i+lt]))
		i += lt
		if i+1 >= len(input) {
			out.WriteString("&lt;")
			i++
			continue
		}
		// Skip comments and CDATA — never resurrect them.
		if strings.HasPrefix(input[i:], "<!--") {
			end := strings.Index(input[i+4:], "-->")
			if end < 0 {
				i = len(input)
				continue
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(input[i:], "<![CDATA[") {
			end := strings.Index(input[i+9:], "]]>")
			if end < 0 {
				i = len(input)
				continue
			}
			i += 9 + end + 3
			continue
		}
		if strings.HasPrefix(input[i:], "<!") || strings.HasPrefix(input[i:], "<?") {
			end := strings.IndexByte(input[i:], '>')
			if end < 0 {
				i = len(input)
				continue
			}
			i += end + 1
			continue
		}
		gt := strings.IndexByte(input[i+1:], '>')
		if gt < 0 {
			out.WriteString("&lt;")
			i++
			continue
		}
		raw := input[i+1 : i+1+gt]
		i += 1 + gt + 1
		closing := false
		if len(raw) > 0 && raw[0] == '/' {
			closing = true
			raw = raw[1:]
		}
		selfClose := false
		if strings.HasSuffix(raw, "/") {
			selfClose = true
			raw = strings.TrimSuffix(raw, "/")
		}
		tag, rest := splitTagName(raw)
		if tag == "" {
			continue
		}
		tag = strings.ToLower(tag)
		attrs, ok := policy.AllowedTags[tag]
		if !ok {
			// Drop entirely — including for well-known dangerous tags where we
			// also want to skip any embedded body so <script>evil()</script>
			// text does not leak.
			if !closing && isRawTextTag(tag) {
				if end := findRawTextEnd(input, i, tag); end >= 0 {
					i = end
				}
			}
			continue
		}
		if closing {
			// Pop the matching tag from the stack. If it does not match we
			// silently drop the tag to keep the tree balanced.
			for j := len(openStack) - 1; j >= 0; j-- {
				if openStack[j] == tag {
					openStack = openStack[:j]
					depth--
					out.WriteString("</")
					out.WriteString(tag)
					out.WriteString(">")
					break
				}
			}
			continue
		}
		if depth >= policy.MaxDepth {
			continue
		}
		safeAttrs := filterAttributes(tag, rest, attrs, policy)
		if _, void := selfClosingTags[tag]; void || selfClose {
			out.WriteString("<")
			out.WriteString(tag)
			if safeAttrs != "" {
				out.WriteString(" ")
				out.WriteString(safeAttrs)
			}
			if void {
				out.WriteString(">")
			} else {
				out.WriteString(" />")
			}
			continue
		}
		out.WriteString("<")
		out.WriteString(tag)
		if safeAttrs != "" {
			out.WriteString(" ")
			out.WriteString(safeAttrs)
		}
		out.WriteString(">")
		openStack = append(openStack, tag)
		depth++
	}
	// Close any tags left open.
	for j := len(openStack) - 1; j >= 0; j-- {
		out.WriteString("</")
		out.WriteString(openStack[j])
		out.WriteString(">")
	}
	return out.String()
}

// SanitizeToPlainText strips all tags and returns the text content only.
func SanitizeToPlainText(input string) string {
	var out strings.Builder
	i := 0
	for i < len(input) {
		lt := strings.IndexByte(input[i:], '<')
		if lt < 0 {
			out.WriteString(input[i:])
			break
		}
		out.WriteString(input[i : i+lt])
		i += lt
		gt := strings.IndexByte(input[i:], '>')
		if gt < 0 {
			break
		}
		i += gt + 1
	}
	return out.String()
}

func splitTagName(raw string) (tag, rest string) {
	raw = strings.TrimLeft(raw, " \t\r\n")
	end := len(raw)
	for i, r := range raw {
		if unicode.IsSpace(r) || r == '/' {
			end = i
			break
		}
	}
	return raw[:end], strings.TrimSpace(raw[end:])
}

func filterAttributes(tag, rest string, allowed map[string]struct{}, policy Policy) string {
	if rest == "" {
		return ""
	}
	matches := tagAttrPattern.FindAllStringSubmatch(rest, -1)
	var kept []string
	var href string
	for _, m := range matches {
		name := strings.ToLower(m[1])
		if strings.HasPrefix(name, eventAttrPrefix) {
			continue
		}
		if name == "style" {
			continue
		}
		if name == "xmlns" || strings.HasPrefix(name, "xmlns:") {
			continue
		}
		value := firstNonEmptyString(m[2], m[3], m[4])
		value = decodeAttrValue(value)
		if _, urlAttr := urlAttributes[name]; urlAttr {
			cleaned := safeURL(value, policy)
			if cleaned == "" {
				continue
			}
			value = cleaned
			if name == "href" {
				href = cleaned
			}
		}
		if _, ok := allowed[name]; !ok {
			continue
		}
		kept = append(kept, name+`="`+attrEscape(value)+`"`)
	}
	joined := strings.Join(kept, " ")
	if tag == "a" {
		tail := hrefTail(href, policy)
		if tail != "" {
			joined = joined + tail
		}
	}
	return joined
}

func hrefTail(href string, policy Policy) string {
	var extras []string
	if href != "" {
		if policy.ForceNoFollow {
			extras = append(extras, `rel="nofollow noopener"`)
		}
		if policy.ForceTargetBlank && (strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://")) {
			extras = append(extras, `target="_blank"`)
		}
	}
	if len(extras) == 0 {
		return ""
	}
	return " " + strings.Join(extras, " ")
}

func safeURL(value string, policy Policy) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Reject control characters that could be used to smuggle a scheme past a
	// naive check.
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	// Relative URLs (path, query, fragment) are permitted unconditionally so
	// long as they do not smell like schemes.
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "?") || strings.HasPrefix(value, "#") {
		return value
	}
	// Extract the scheme, if any.
	colon := strings.IndexByte(value, ':')
	slash := strings.IndexByte(value, '/')
	if colon < 0 || (slash >= 0 && slash < colon) {
		// No scheme, treat as relative.
		return value
	}
	scheme := strings.ToLower(value[:colon])
	if _, ok := policy.AllowedURLSchemes[scheme]; !ok {
		return ""
	}
	return value
}

func isRawTextTag(tag string) bool {
	switch tag {
	case "script", "style", "xmp", "iframe", "noembed", "noframes":
		return true
	default:
		return false
	}
}

func findRawTextEnd(input string, start int, tag string) int {
	needle := "</" + tag
	rest := strings.ToLower(input[start:])
	idx := strings.Index(rest, needle)
	if idx < 0 {
		return len(input)
	}
	end := start + idx + len(needle)
	if end >= len(input) {
		return len(input)
	}
	gt := strings.IndexByte(input[end:], '>')
	if gt < 0 {
		return len(input)
	}
	return end + gt + 1
}

func escapeText(text string) string {
	return strings.NewReplacer(
		`&`, "&amp;",
		`<`, "&lt;",
		`>`, "&gt;",
	).Replace(text)
}

func attrEscape(value string) string {
	return strings.NewReplacer(
		`&`, "&amp;",
		`"`, "&quot;",
		`<`, "&lt;",
		`>`, "&gt;",
	).Replace(value)
}

func decodeAttrValue(value string) string {
	// Minimal decoding: recognise &amp; &quot; &lt; &gt; &#xNN; &#NN; so a
	// smuggled &#106;avascript: is caught by the scheme filter.
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = strings.ReplaceAll(value, "&quot;", `"`)
	value = strings.ReplaceAll(value, "&#34;", `"`)
	value = strings.ReplaceAll(value, "&lt;", "<")
	value = strings.ReplaceAll(value, "&gt;", ">")
	// Numeric character references &#NN; or &#xNN;
	value = numericEntityRE.ReplaceAllStringFunc(value, func(match string) string {
		body := match[2 : len(match)-1]
		var n int
		if len(body) > 0 && (body[0] == 'x' || body[0] == 'X') {
			for _, r := range body[1:] {
				switch {
				case r >= '0' && r <= '9':
					n = n*16 + int(r-'0')
				case r >= 'a' && r <= 'f':
					n = n*16 + int(r-'a'+10)
				case r >= 'A' && r <= 'F':
					n = n*16 + int(r-'A'+10)
				default:
					return match
				}
			}
		} else {
			for _, r := range body {
				if r < '0' || r > '9' {
					return match
				}
				n = n*10 + int(r-'0')
			}
		}
		if n <= 0 || n > 0x10FFFF {
			return match
		}
		return string(rune(n))
	})
	return value
}

var numericEntityRE = regexp.MustCompile(`&#[xX]?[0-9a-fA-F]+;`)

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
