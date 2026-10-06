// Package slackmd converts Slack mrkdwn text into HTML and plain text for
// Matrix m.text events.
package slackmd

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

var tagRegex = regexp.MustCompile(`(?s)<[^>]*>`)
var whitespaceRegex = regexp.MustCompile(`[ \t]*\n[ \t]*`)

// SlackToHTML converts Slack message text (mrkdwn format) into a plain text
// body and an HTML formatted body. The plain text has Slack links expanded
// (label + url), the HTML has proper tags.
func SlackToHTML(text string) (plain string, htmlOut string) {
	text = unescapeEntities(text)
	var plainBuf, htmlBuf strings.Builder
	for i, part := range strings.Split(text, "```") {
		if i%2 == 1 {
			code := stripFenceLang(part)
			plainBuf.WriteString(code)
			htmlBuf.WriteString("<pre><code>")
			htmlBuf.WriteString(html.EscapeString(code))
			htmlBuf.WriteString("</code></pre>")
			continue
		}
		convertSegment(part, &plainBuf, &htmlBuf)
	}
	return plainBuf.String(), htmlBuf.String()
}

// StripHTML removes HTML tags, returning unescaped plain text.
func StripHTML(text string) string {
	stripped := tagRegex.ReplaceAllString(text, "")
	stripped = html.UnescapeString(stripped)
	return whitespaceRegex.ReplaceAllString(strings.TrimSpace(stripped), "\n")
}

func unescapeEntities(text string) string {
	replacer := strings.NewReplacer(
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
		"&amp;", "&",
	)
	return replacer.Replace(text)
}

func stripFenceLang(fence string) string {
	if idx := strings.Index(fence, "\n"); idx >= 0 {
		fence = fence[idx+1:]
	}
	return strings.TrimSuffix(fence, "\n")
}

var emphasisTags = map[rune][2]string{
	'*': {"<strong>", "</strong>"},
	'_': {"<em>", "</em>"},
	'~': {"<del>", "</del>"},
}

const boundaryBefore = "([{<>*_~"
const boundaryAfter = ".,!?)[]{}<>*_~'\";:"

func isBoundaryBefore(r rune) bool {
	return r == 0 || unicode.IsSpace(r) || strings.ContainsRune(boundaryBefore, r)
}

func isBoundaryAfter(r rune) bool {
	return r == 0 || unicode.IsSpace(r) || strings.ContainsRune(boundaryAfter, r)
}

func convertSegment(seg string, plainBuf, htmlBuf *strings.Builder) {
	runes := []rune(seg)
	for i := 0; i < len(runes); {
		cur := runes[i]
		switch cur {
		case '`':
			if end := findRune(runes[i+1:], '`', true); end >= 0 {
				code := string(runes[i+1 : i+1+end])
				plainBuf.WriteString(code)
				htmlBuf.WriteString("<code>")
				htmlBuf.WriteString(html.EscapeString(code))
				htmlBuf.WriteString("</code>")
				i += end + 2
				continue
			}
		case '<':
			if gt := findRune(runes[i+1:], '>', true); gt >= 0 {
				inner := string(runes[i+1 : i+1+gt])
				if convertLink(inner, plainBuf, htmlBuf) {
					i += gt + 2
					continue
				}
			}
		case '*', '_', '~':
			if end := findEmphasisEnd(runes, i, cur); end >= 0 {
				content := string(runes[i+1 : end])
				tags := emphasisTags[cur]
				plainBuf.WriteString(content)
				htmlBuf.WriteString(tags[0])
				htmlBuf.WriteString(html.EscapeString(content))
				htmlBuf.WriteString(tags[1])
				i = end + 1
				continue
			}
		case '\n':
			plainBuf.WriteRune('\n')
			htmlBuf.WriteString("<br>\n")
			i++
			continue
		}
		htmlBuf.WriteString(html.EscapeString(string(cur)))
		plainBuf.WriteRune(cur)
		i++
	}
}

// findRune returns the index of needle in runes, or -1. If stopAtNewline is
// true the search aborts at a newline.
func findRune(runes []rune, needle rune, stopAtNewline bool) int {
	for i, r := range runes {
		if r == needle {
			return i
		}
		if stopAtNewline && r == '\n' {
			return -1
		}
	}
	return -1
}

// findEmphasisEnd finds the closing marker for emphasis started at start.
func findEmphasisEnd(runes []rune, start int, marker rune) int {
	if !isBoundaryBefore(boundaryPrev(runes, start)) {
		return -1
	}
	if start+1 >= len(runes) {
		return -1
	}
	next := runes[start+1]
	if next == marker || unicode.IsSpace(next) {
		return -1
	}
	for j := start + 1; j < len(runes); j++ {
		if runes[j] == '\n' {
			return -1
		}
		if runes[j] != marker {
			continue
		}
		if unicode.IsSpace(runes[j-1]) {
			return -1
		}
		var after rune
		if j+1 < len(runes) {
			after = runes[j+1]
		}
		if isBoundaryAfter(after) {
			return j
		}
	}
	return -1
}

func boundaryPrev(runes []rune, i int) rune {
	if i == 0 {
		return 0
	}
	return runes[i-1]
}

func convertLink(inner string, plainBuf, htmlBuf *strings.Builder) bool {
	url, label := inner, ""
	hasLabel := false
	if idx := strings.Index(inner, "|"); idx >= 0 {
		url, label = inner[:idx], inner[idx+1:]
		hasLabel = true
	}
	if !isURL(url) {
		return false
	}
	if !hasLabel {
		label = url
		if strings.HasPrefix(url, "mailto:") {
			label = strings.TrimPrefix(url, "mailto:")
		}
	}
	plainBuf.WriteString(label)
	if label != url {
		plainBuf.WriteString(" (")
		plainBuf.WriteString(url)
		plainBuf.WriteString(")")
	}
	htmlBuf.WriteString(`<a href="`)
	htmlBuf.WriteString(html.EscapeString(url))
	htmlBuf.WriteString(`">`)
	htmlBuf.WriteString(html.EscapeString(label))
	htmlBuf.WriteString("</a>")
	return true
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "ftp://") ||
		strings.HasPrefix(s, "mailto:")
}
