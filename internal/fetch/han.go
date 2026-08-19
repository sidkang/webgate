package fetch

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Non-semantic Han.css wrappers whose children should be preserved.
var hanUnwrapTags = map[string]struct{}{
	"h-char":       {},
	"h-inner":      {},
	"h-char-group": {},
	"h-word":       {},
	"h-jinze":      {},
}

// Synthetic Han.css spacing/control nodes — drop entirely.
var hanDropTags = map[string]struct{}{
	"h-hws": {},
	"h-cs":  {},
}

var hanWrapperRE = regexp.MustCompile(`(?i)<h-(?:char(?:-group)?|inner|word|jinze|hws|cs)\b`)

// HasHanTypographyWrappers is a cheap precheck before parsing.
func HasHanTypographyWrappers(s string) bool {
	return hanWrapperRE.MatchString(s)
}

// NormalizeHanTypographyHTML unwraps/drops known Han typography wrappers.
// Returns the original string when no matching tags/elements exist.
func NormalizeHanTypographyHTML(raw string) string {
	if raw == "" || !HasHanTypographyWrappers(raw) {
		return raw
	}
	nodes, err := html.ParseFragment(strings.NewReader(raw), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil || len(nodes) == 0 {
		return raw
	}
	// Wrap in a synthetic root for walking/serialization.
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	if !documentHasHanElements(root) {
		return raw
	}
	changed := unwrapHanNodes(root)
	if changed == 0 {
		return raw
	}
	var buf bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	return buf.String()
}

func documentHasHanElements(n *html.Node) bool {
	if n.Type == html.ElementNode {
		name := strings.ToLower(n.Data)
		if _, ok := hanUnwrapTags[name]; ok {
			return true
		}
		if _, ok := hanDropTags[name]; ok {
			return true
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if documentHasHanElements(c) {
			return true
		}
	}
	return false
}

type hanAction struct {
	node   *html.Node
	action string // unwrap | drop
}

func collectHanNodes(n *html.Node, out *[]hanAction) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		collectHanNodes(c, out)
		c = next
	}
	if n.Type != html.ElementNode {
		return
	}
	name := strings.ToLower(n.Data)
	if _, ok := hanDropTags[name]; ok {
		*out = append(*out, hanAction{node: n, action: "drop"})
		return
	}
	if _, ok := hanUnwrapTags[name]; ok {
		*out = append(*out, hanAction{node: n, action: "unwrap"})
	}
}

func unwrapHanNodes(root *html.Node) int {
	var actions []hanAction
	collectHanNodes(root, &actions)
	changed := 0
	for _, a := range actions {
		parent := a.node.Parent
		if parent == nil {
			continue
		}
		if a.action == "drop" {
			parent.RemoveChild(a.node)
			changed++
			continue
		}
		for a.node.FirstChild != nil {
			child := a.node.FirstChild
			a.node.RemoveChild(child)
			parent.InsertBefore(child, a.node)
		}
		parent.RemoveChild(a.node)
		changed++
	}
	return changed
}
