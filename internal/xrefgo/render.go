package xrefgo

import (
	"path/filepath"
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Only literal Rails render targets become template edges. A dynamic argument
// stays visible as a gap; evaluating Ruby expressions would invent a target.
func renderFromCall(path string, src []byte, n *ts.Node, owner, source string, class bool) (Render, bool) {
	receiver := n.ChildByFieldName("receiver")
	if receiver != nil && receiver.Kind() != "self" {
		return Render{}, false
	}
	r := Render{Owner: owner, Source: source, SourceClass: class, Path: path, Line: int(n.StartPosition().Row) + 1, Kind: "default"}
	args := n.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return r, true
	}
	for _, kind := range []string{"template", "action", "partial", "file"} {
		if v := renderOption(args, src, kind); v != nil {
			r.Kind = kind
			r.Target = literalRenderTarget(v, src)
			if r.Target == "" {
				r.Kind = "dynamic"
			}
			return r, true
		}
	}
	for _, kind := range []string{"json", "html", "plain", "body", "inline", "nothing"} {
		if renderOption(args, src, kind) != nil {
			r.Kind = "response"
			return r, true
		}
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "pair" {
			continue
		}
		r.Target = literalRenderTarget(arg, src)
		if r.Target == "" {
			r.Kind = "dynamic"
		} else {
			r.Kind = "action"
		}
		return r, true
	}
	return r, true
}

func renderOption(args *ts.Node, src []byte, key string) *ts.Node {
	for i := uint(0); i < args.NamedChildCount(); i++ {
		pair := args.NamedChild(i)
		if pair.Kind() == "pair" && strings.TrimSuffix(fieldText(pair, "key", src), ":") == key {
			return pair.ChildByFieldName("value")
		}
	}
	return nil
}

func literalRenderTarget(n *ts.Node, src []byte) string {
	if n == nil {
		return ""
	}
	if n.Kind() != "string" && n.Kind() != "simple_symbol" {
		return ""
	}
	s := n.Utf8Text(src)
	if strings.Contains(s, "#{") || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	s = unquoteRoute(s)
	if s == "" || strings.Contains(s, "..") || strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}

func renderViewPath(controller string, r Render) string {
	if r.Kind != "action" && r.Kind != "template" && r.Kind != "partial" {
		return ""
	}
	target := r.Target
	if !strings.Contains(target, "/") {
		target = controller + "/" + target
	}
	if r.Kind == "partial" {
		dir, base := filepath.Split(target)
		target = dir + "_" + base
	}
	return "app/views/" + target
}

func viewRefs(files map[string]File, prefix string) []Ref {
	if prefix == "" {
		return nil
	}
	out := []Ref{}
	for path, file := range files {
		if strings.HasPrefix(path, prefix+".") && (strings.HasSuffix(path, ".erb") || strings.HasSuffix(path, ".haml")) {
			out = append(out, file.Refs...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}
