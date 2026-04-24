package webfetch

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ExtractTextFromHTML extracts plain text from HTML, skipping scripts/styles.
func ExtractTextFromHTML(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return htmlContent
	}

	var textBuilder strings.Builder
	skipTags := map[string]bool{
		"script": true, "style": true, "noscript": true,
		"iframe": true, "object": true, "embed": true,
	}

	var extractText func(*html.Node)
	extractText = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				textBuilder.WriteString(text)
				textBuilder.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c)
		}
	}

	extractText(doc)
	return strings.TrimSpace(textBuilder.String())
}

// ConvertHTMLToMarkdown converts HTML to simplified Markdown.
func ConvertHTMLToMarkdown(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return htmlContent
	}

	var mdBuilder strings.Builder
	skipTags := map[string]bool{
		"script": true, "style": true, "meta": true, "link": true,
	}

	var convertNode func(*html.Node, int)
	convertNode = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}

		switch n.Type {
		case html.TextNode:
			text := regexp.MustCompile(`\s+`).ReplaceAllString(n.Data, " ")
			if strings.TrimSpace(text) != "" {
				mdBuilder.WriteString(text)
			}
		case html.ElementNode:
			switch n.Data {
			case "h1":
				mdBuilder.WriteString("\n\n# ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "h2":
				mdBuilder.WriteString("\n\n## ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "h3":
				mdBuilder.WriteString("\n\n### ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "h4":
				mdBuilder.WriteString("\n\n#### ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "h5":
				mdBuilder.WriteString("\n\n##### ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "h6":
				mdBuilder.WriteString("\n\n###### ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "p":
				mdBuilder.WriteString("\n\n")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			case "br":
				mdBuilder.WriteString("\n")
			case "hr":
				mdBuilder.WriteString("\n\n---\n\n")
			case "strong", "b":
				mdBuilder.WriteString("**")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("**")
				return
			case "em", "i":
				mdBuilder.WriteString("*")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("*")
				return
			case "code":
				mdBuilder.WriteString("`")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("`")
				return
			case "pre":
				mdBuilder.WriteString("\n\n```\n")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n```\n\n")
				return
			case "a":
				href := getAttr(n, "href")
				mdBuilder.WriteString("[")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("](")
				mdBuilder.WriteString(href)
				mdBuilder.WriteString(")")
				return
			case "img":
				alt := getAttr(n, "alt")
				src := getAttr(n, "src")
				mdBuilder.WriteString("![")
				mdBuilder.WriteString(alt)
				mdBuilder.WriteString("](")
				mdBuilder.WriteString(src)
				mdBuilder.WriteString(")")
			case "ul", "ol":
				mdBuilder.WriteString("\n")
				processChildren(n, &mdBuilder, convertNode, depth+1)
				mdBuilder.WriteString("\n")
				return
			case "li":
				mdBuilder.WriteString("\n- ")
				processChildren(n, &mdBuilder, convertNode, depth)
				return
			case "blockquote":
				mdBuilder.WriteString("\n\n> ")
				processChildren(n, &mdBuilder, convertNode, depth)
				mdBuilder.WriteString("\n\n")
				return
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			convertNode(c, depth)
		}
	}

	convertNode(doc, 0)

	result := mdBuilder.String()
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

func processChildren(n *html.Node, builder *strings.Builder, convertFn func(*html.Node, int), depth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		convertFn(c, depth)
	}
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}
