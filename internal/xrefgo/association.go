package xrefgo

import (
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Association declarations are syntax evidence. Only a named target already
// present in the model map is accepted; a Rails runtime association is not
// assumed from a similar-looking method name alone.
func scanModelAssociations(path string, src []byte, models map[string]ModelInfo) []ModelAssociation {
	if !strings.HasPrefix(path, "app/models/") || !strings.HasSuffix(path, ".rb") {
		return nil
	}
	p, tree, err := rubyTree(src)
	if err != nil {
		return nil
	}
	defer p.Close()
	defer tree.Close()
	concern := strings.HasPrefix(path, "app/models/concerns/")
	out := []ModelAssociation{}
	var walk func(*ts.Node, string, bool)
	walk = func(n *ts.Node, owner string, included bool) {
		switch n.Kind() {
		case "class", "module":
			name := fieldText(n, "name", src)
			if name != "" {
				if owner != "" && !strings.Contains(name, "::") {
					name = owner + "::" + name
				}
				owner = strings.TrimPrefix(name, "::")
			}
		case "method", "singleton_method":
			return
		case "call":
			method := fieldText(n, "method", src)
			if concern && method == "included" && n.ChildByFieldName("receiver") == nil {
				for i := uint(0); i < n.NamedChildCount(); i++ {
					if child := n.NamedChild(i); child.Kind() == "do_block" {
						walk(child, owner, true)
					}
				}
				return
			}
			if owner != "" && (!concern && models[owner].Table != "" || concern && included) && n.ChildByFieldName("receiver") == nil && n.Parent() != nil && n.Parent().Kind() == "body_statement" && (method == "has_many" || method == "has_one" || method == "belongs_to") {
				args := n.ChildByFieldName("arguments")
				if args != nil && args.NamedChildCount() > 0 {
					first := args.NamedChild(0)
					if first.Kind() == "simple_symbol" || first.Kind() == "string" {
						name := unquoteRoute(first.Utf8Text(src))
						target := ""
						through := false
						unknownClassName := false
						for i := uint(1); i < args.NamedChildCount(); i++ {
							pair := args.NamedChild(i)
							if pair.Kind() != "pair" {
								continue
							}
							key := strings.TrimSuffix(fieldText(pair, "key", src), ":")
							if key == "through" {
								through = true
							}
							if key == "class_name" {
								if v := pair.ChildByFieldName("value"); v != nil && v.Kind() == "string" {
									target = strings.TrimPrefix(unquoteRoute(v.Utf8Text(src)), "::")
								} else {
									unknownClassName = true
								}
							}
						}
						if !through && !unknownClassName {
							if target == "" {
								base := name
								if method == "has_many" {
									base = singularRoute(base)
								}
								target = camelRoute(base)
							}
							if models[target].Table != "" {
								out = append(out, ModelAssociation{Owner: owner, Name: name, Target: target, Path: path, Line: int(n.StartPosition().Row) + 1})
							}
						}
					}
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), owner, included)
		}
	}
	walk(tree.RootNode(), "", false)
	return out
}

func modelAssociations(idx *Index) map[string]map[string][]ModelAssociation {
	out := map[string]map[string][]ModelAssociation{}
	modules := map[string][]ModelAssociation{}
	for _, f := range idx.Files {
		for _, a := range f.Associations {
			if strings.HasPrefix(a.Path, "app/models/concerns/") {
				modules[a.Owner] = append(modules[a.Owner], a)
			} else {
				if out[a.Owner] == nil {
					out[a.Owner] = map[string][]ModelAssociation{}
				}
				out[a.Owner][a.Name] = append(out[a.Owner][a.Name], a)
			}
		}
	}
	for _, f := range idx.Files {
		for _, inc := range f.Includes {
			module := inc.Module
			if len(modules[module]) == 0 && !strings.Contains(module, "::") && strings.Contains(inc.Owner, "::") {
				module = inc.Owner[:strings.LastIndex(inc.Owner, "::")] + "::" + module
			}
			for _, a := range modules[module] {
				if out[inc.Owner] == nil {
					out[inc.Owner] = map[string][]ModelAssociation{}
				}
				out[inc.Owner][a.Name] = append(out[inc.Owner][a.Name], a)
			}
		}
	}
	return out
}

func associationReceiver(c Call, locals map[string][]LocalBinding, models map[string]ModelInfo) string {
	switch c.ReceiverKind {
	case "model_helper", "constructed":
		owner := strings.TrimSuffix(c.Receiver, ".new")
		if c.ReceiverKind == "model_helper" {
			if len(locals[c.Receiver]) != 0 {
				return ""
			}
			owner = camelRoute(strings.TrimPrefix(c.Receiver, "current_"))
		}
		if models[owner].Table != "" {
			return owner
		}
	case "variable":
		bindings := locals[c.Receiver]
		if c.Direct && len(bindings) == 1 && bindings[0].Target != "" && !bindings[0].Class && bindings[0].Line < c.Line && models[bindings[0].Target].Table != "" {
			return bindings[0].Target
		}
	}
	return ""
}
