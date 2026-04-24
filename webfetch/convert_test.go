package webfetch

import (
	"strings"
	"testing"
)

func TestExtractTextFromHTML(t *testing.T) {
	cases := []struct {
		name     string
		html     string
		expected string
	}{
		{
			name:     "simple text",
			html:     "<p>Hello World</p>",
			expected: "Hello World",
		},
		{
			name:     "nested elements",
			html:     "<div><p>Hello</p><p>World</p></div>",
			expected: "Hello World",
		},
		{
			name:     "skips script",
			html:     "<div>Hello<script>alert('bad')</script>World</div>",
			expected: "Hello World",
		},
		{
			name:     "skips style",
			html:     "<div>Hello<style>.foo{color:red}</style>World</div>",
			expected: "Hello World",
		},
		{
			name:     "preserves text from links",
			html:     "<p>Click <a href='#'>here</a> please</p>",
			expected: "Click here please",
		},
		{
			name:     "handles whitespace",
			html:     "<p>  Hello   World  </p>",
			expected: "Hello   World", // preserves internal whitespace, trims outer
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ExtractTextFromHTML(tc.html)
			if result != tc.expected {
				t.Errorf("ExtractTextFromHTML(%q) = %q, want %q", tc.html, result, tc.expected)
			}
		})
	}
}

func TestConvertHTMLToMarkdown(t *testing.T) {
	cases := []struct {
		name     string
		html     string
		contains []string
	}{
		{
			name:     "heading h1",
			html:     "<h1>Title</h1>",
			contains: []string{"# Title"},
		},
		{
			name:     "heading h2",
			html:     "<h2>Subtitle</h2>",
			contains: []string{"## Subtitle"},
		},
		{
			name:     "paragraph",
			html:     "<p>Hello World</p>",
			contains: []string{"Hello World"},
		},
		{
			name:     "bold",
			html:     "<p><strong>Bold</strong> text</p>",
			contains: []string{"**Bold**"},
		},
		{
			name:     "italic",
			html:     "<p><em>Italic</em> text</p>",
			contains: []string{"*Italic*"},
		},
		{
			name:     "code inline",
			html:     "<p>Use <code>fmt.Println</code> function</p>",
			contains: []string{"`fmt.Println`"},
		},
		{
			name:     "code block",
			html:     "<pre>func main() {}</pre>",
			contains: []string{"```", "func main() {}"},
		},
		{
			name:     "link",
			html:     `<p>Visit <a href="https://example.com">Example</a></p>`,
			contains: []string{"[Example](https://example.com)"},
		},
		{
			name:     "unordered list",
			html:     "<ul><li>Item 1</li><li>Item 2</li></ul>",
			contains: []string{"- Item 1", "- Item 2"},
		},
		{
			name:     "blockquote",
			html:     "<blockquote>A quote</blockquote>",
			contains: []string{"> A quote"},
		},
		{
			name:     "horizontal rule",
			html:     "<p>Above</p><hr><p>Below</p>",
			contains: []string{"---"},
		},
		{
			name:     "skips script",
			html:     "<p>Hello</p><script>bad()</script><p>World</p>",
			contains: []string{"Hello", "World"},
		},
		{
			name:     "skips style",
			html:     "<style>.foo{}</style><p>Content</p>",
			contains: []string{"Content"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ConvertHTMLToMarkdown(tc.html)
			for _, expected := range tc.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("ConvertHTMLToMarkdown(%q) = %q, want to contain %q", tc.html, result, expected)
				}
			}
			// Verify script/style content is not in output
			if tc.name == "skips script" && strings.Contains(result, "bad()") {
				t.Error("script content should be stripped")
			}
			if tc.name == "skips style" && strings.Contains(result, ".foo") {
				t.Error("style content should be stripped")
			}
		})
	}
}

func TestConvertHTMLToMarkdown_ComplexDocument(t *testing.T) {
	html := `
<!DOCTYPE html>
<html>
<head>
	<title>Test Page</title>
	<style>.hidden { display: none; }</style>
	<script>console.log('init')</script>
</head>
<body>
	<h1>Main Title</h1>
	<p>This is a <strong>paragraph</strong> with <em>formatting</em>.</p>
	<h2>Section</h2>
	<ul>
		<li>First item</li>
		<li>Second item</li>
	</ul>
	<p>Visit <a href="https://example.com">our site</a> for more.</p>
	<pre>
code block
here
	</pre>
</body>
</html>
`

	result := ConvertHTMLToMarkdown(html)

	expected := []string{
		"# Main Title",
		"**paragraph**",
		"*formatting*",
		"## Section",
		"- First item",
		"- Second item",
		"[our site](https://example.com)",
		"```",
	}

	for _, exp := range expected {
		if !strings.Contains(result, exp) {
			t.Errorf("expected result to contain %q, got:\n%s", exp, result)
		}
	}

	// Should not contain script or style content
	if strings.Contains(result, "console.log") {
		t.Error("should not contain script content")
	}
	if strings.Contains(result, "display: none") {
		t.Error("should not contain style content")
	}
}
