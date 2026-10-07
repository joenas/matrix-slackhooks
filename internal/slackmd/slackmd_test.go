package slackmd

import (
	"testing"
)

func TestSlackToHTML(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantPlain string
		wantHTML  string
	}{
		{
			name:      "plain text",
			input:     "Hello world",
			wantPlain: "Hello world",
			wantHTML:  "Hello world",
		},
		{
			name:      "bold",
			input:     "Hello *bold* world",
			wantPlain: "Hello bold world",
			wantHTML:  "Hello <strong>bold</strong> world",
		},
		{
			name:      "italic",
			input:     "Hello _italic_ world",
			wantPlain: "Hello italic world",
			wantHTML:  "Hello <em>italic</em> world",
		},
		{
			name:      "strike",
			input:     "Hello ~strike~ world",
			wantPlain: "Hello strike world",
			wantHTML:  "Hello <del>strike</del> world",
		},
		{
			name:      "inline code",
			input:     "run `make test` now",
			wantPlain: "run make test now",
			wantHTML:  "run <code>make test</code> now",
		},
		{
			name:      "code block keeps its first line",
			input:     "before\n```go\nfmt.Println(\"hi\")\n```\nafter",
			wantPlain: "before\ngo\nfmt.Println(\"hi\")\nafter",
			wantHTML:  "before<br>\n<pre><code>go\nfmt.Println(&#34;hi&#34;)</code></pre><br>\nafter",
		},
		{
			name:      "code block without language",
			input:     "```\nplain code\n```",
			wantPlain: "plain code",
			wantHTML:  "<pre><code>plain code</code></pre>",
		},
		{
			name:      "code block without leading newline",
			input:     "```line1\nline2```",
			wantPlain: "line1\nline2",
			wantHTML:  "<pre><code>line1\nline2</code></pre>",
		},
		{
			name:      "link with label",
			input:     "see <https://example.com|the docs>",
			wantPlain: "see the docs (https://example.com)",
			wantHTML:  `see <a href="https://example.com">the docs</a>`,
		},
		{
			name:      "bare link",
			input:     "see <https://example.com>",
			wantPlain: "see https://example.com",
			wantHTML:  `see <a href="https://example.com">https://example.com</a>`,
		},
		{
			name:      "mailto link",
			input:     "mail <mailto:me@example.com|me>",
			wantPlain: "mail me (mailto:me@example.com)",
			wantHTML:  `mail <a href="mailto:me@example.com">me</a>`,
		},
		{
			name:      "entity unescaping",
			input:     "a &lt; b &amp;&gt; c",
			wantPlain: "a < b &> c",
			wantHTML:  "a &lt; b &amp;&gt; c",
		},
		{
			name:      "non-link angle brackets stay literal",
			input:     "check <!here> and size 3 < 4",
			wantPlain: "check <!here> and size 3 < 4",
			wantHTML:  "check &lt;!here&gt; and size 3 &lt; 4",
		},
		{
			name:      "newlines become breaks",
			input:     "line one\nline two",
			wantPlain: "line one\nline two",
			wantHTML:  "line one<br>\nline two",
		},
		{
			name:      "snake case is not italic",
			input:     "call some_var_name here",
			wantPlain: "call some_var_name here",
			wantHTML:  "call some_var_name here",
		},
		{
			name:      "multiplication is not bold",
			input:     "compute 2*3*4 = 24",
			wantPlain: "compute 2*3*4 = 24",
			wantHTML:  "compute 2*3*4 = 24",
		},
		{
			name:      "emphasis at end of sentence",
			input:     "this is *great*!",
			wantPlain: "this is great!",
			wantHTML:  "this is <strong>great</strong>!",
		},
		{
			name:      "html in input is escaped",
			input:     "hello <script>alert(1)</script> world",
			wantPlain: "hello <script>alert(1)</script> world",
			wantHTML:  "hello &lt;script&gt;alert(1)&lt;/script&gt; world",
		},
		{
			name:      "combined",
			input:     "*Bold* and _italics_ with `code` and <https://example.com|link>",
			wantPlain: "Bold and italics with code and link (https://example.com)",
			wantHTML:  `<strong>Bold</strong> and <em>italics</em> with <code>code</code> and <a href="https://example.com">link</a>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plain, html := SlackToHTML(test.input)
			if plain != test.wantPlain {
				t.Errorf("plain: got %q, want %q", plain, test.wantPlain)
			}
			if html != test.wantHTML {
				t.Errorf("html: got %q, want %q", html, test.wantHTML)
			}
		})
	}
}

func TestSlackToHTMLEmphasisInsideCodeNotProcessed(t *testing.T) {
	plain, html := SlackToHTML("`*not bold*` but *yes*")
	if plain != "*not bold* but yes" {
		t.Errorf("plain: got %q", plain)
	}
	want := "<code>*not bold*</code> but <strong>yes</strong>"
	if html != want {
		t.Errorf("html: got %q, want %q", html, want)
	}
}

func TestStripHTML(t *testing.T) {
	tests := map[string]string{
		"<b>bold</b>":                         "bold",
		`<a href="https://example.com">x</a>`: "x",
		"one<br>two":                          "onetwo",
		"hello &amp; <i>world</i>":            "hello & world",
		"no tags":                             "no tags",
	}
	for input, want := range tests {
		if got := StripHTML(input); got != want {
			t.Errorf("StripHTML(%q): got %q, want %q", input, got, want)
		}
	}
}
