package xrefgo

import (
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Scan only literal controller class declarations. Dynamic inheritance and
// classes supplied by gems remain outside the local source index.
func scanControllerClasses(path string, src []byte) []ClassDecl {
	if !strings.HasPrefix(path, "app/controllers/") || !strings.HasSuffix(path, ".rb") {
		return nil
	}
	p, tree, err := rubyTree(src)
	if err != nil {
		return nil
	}
	defer p.Close()
	defer tree.Close()
	out := []ClassDecl{}
	var walk func(*ts.Node, string)
	walk = func(n *ts.Node, namespace string) {
		switch n.Kind() {
		case "module", "class":
			name := fieldText(n, "name", src)
			if name != "" {
				if namespace != "" && !strings.Contains(name, "::") {
					name = namespace + "::" + name
				}
				name = strings.TrimPrefix(name, "::")
				if n.Kind() == "class" {
					decl := ClassDecl{Owner: name, Path: path, Line: int(n.StartPosition().Row) + 1}
					if superclass := n.ChildByFieldName("superclass"); superclass != nil && superclass.NamedChildCount() == 1 {
						child := superclass.NamedChild(0)
						if child.Kind() == "constant" || child.Kind() == "scope_resolution" {
							decl.Superclass = strings.TrimPrefix(child.Utf8Text(src), "::")
						}
					}
					out = append(out, decl)
				}
				namespace = name
			}
		case "method", "singleton_method":
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), namespace)
		}
	}
	walk(tree.RootNode(), "")
	return out
}
