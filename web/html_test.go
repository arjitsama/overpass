package web

import (
	"io/fs"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func readAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(FS(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func parseHTML(t *testing.T) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(readAsset(t, "index.html")))
	if err != nil {
		t.Fatalf("parse index.html: %v", err)
	}
	return doc
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func textOf(n *html.Node) string {
	var b strings.Builder
	walk(n, func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
	})
	return strings.TrimSpace(b.String())
}

// Acceptance 2: structural accessibility invariants of the served page.
func TestHTMLStructure(t *testing.T) {
	doc := parseHTML(t)

	// lang is set on <html>.
	var htmlNode *html.Node
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Html {
			htmlNode = n
		}
	})
	if htmlNode == nil {
		t.Fatal("no <html> element")
	}
	if lang, ok := attr(htmlNode, "lang"); !ok || strings.TrimSpace(lang) == "" {
		t.Error("<html> is missing a non-empty lang attribute")
	}

	// Exactly one <h1>, and no skipped heading levels.
	var levels []int
	h1s := 0
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			lvl := int(n.Data[1] - '0')
			levels = append(levels, lvl)
			if n.DataAtom == atom.H1 {
				h1s++
			}
		}
	})
	if h1s != 1 {
		t.Errorf("want exactly one <h1>, got %d", h1s)
	}
	prev := 0
	for _, lvl := range levels {
		if prev != 0 && lvl > prev+1 {
			t.Errorf("heading level jumps from h%d to h%d (skipped a level)", prev, lvl)
		}
		prev = lvl
	}

	// Every <button> and <input> has an accessible name.
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.DataAtom == atom.Button || n.DataAtom == atom.Input {
			if !hasAccessibleName(n) {
				id, _ := attr(n, "id")
				t.Errorf("%s#%s has no accessible name", n.Data, id)
			}
		}
	})

	// Every <table> has a <caption>.
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.DataAtom != atom.Table {
			return
		}
		hasCaption := false
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.Caption {
				hasCaption = true
			}
		}
		if !hasCaption {
			t.Error("a <table> is missing a <caption>")
		}
	})

	// No inline click handler, and no clickable non-button/link (role=button on a div/span).
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if _, ok := attr(n, "onclick"); ok {
			t.Errorf("<%s> has an inline onclick handler", n.Data)
		}
		if role, ok := attr(n, "role"); ok && role == "button" &&
			n.DataAtom != atom.Button && n.DataAtom != atom.A {
			t.Errorf("<%s> has role=button but is not a <button>/<a>", n.Data)
		}
	})
}

func hasAccessibleName(n *html.Node) bool {
	if v, ok := attr(n, "aria-label"); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := attr(n, "aria-labelledby"); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := attr(n, "title"); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if n.DataAtom == atom.Input {
		// A value on a submit/button input, or an associated <label> (by id), names it.
		if typ, _ := attr(n, "type"); typ == "submit" || typ == "button" {
			if v, ok := attr(n, "value"); ok && strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	return textOf(n) != ""
}
