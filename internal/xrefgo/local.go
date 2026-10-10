package xrefgo

import ts "github.com/tree-sitter/go-tree-sitter"

// Bind only an obvious, direct local assignment. All other assignments to the
// same name are kept as blockers so a branch or later reassignment cannot be
// mistaken for a stable receiver type.
func methodBindings(path string, method *ts.Node, src []byte, owner, name string, class bool) []LocalBinding {
	body := method.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	out := []LocalBinding{}
	var walk func(*ts.Node)
	walk = func(n *ts.Node) {
		if n != body && (n.Kind() == "method" || n.Kind() == "singleton_method") {
			return
		}
		if n.Kind() == "assignment" || n.Kind() == "operator_assignment" {
			left := n.ChildByFieldName("left")
			if left != nil && left.Kind() == "identifier" {
				b := LocalBinding{Owner: owner, Source: name, SourceClass: class, Name: left.Utf8Text(src), Path: path, Line: int(n.StartPosition().Row) + 1}
				if n.Kind() == "assignment" && n.Parent() != nil && n.Parent().Equals(*body) {
					right := n.ChildByFieldName("right")
					if right != nil {
						switch right.Kind() {
						case "constant", "scope_resolution":
							b.Target = right.Utf8Text(src)
							b.Class = true
						case "call":
							if fieldText(right, "method", src) == "new" {
								b.Target = callRoot(right.ChildByFieldName("receiver"), src)
							}
						}
					}
				}
				out = append(out, b)
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

func directStatementCall(n *ts.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	if p.Kind() == "body_statement" {
		return true
	}
	if p.Kind() == "assignment" || p.Kind() == "return" {
		return p.Parent() != nil && p.Parent().Kind() == "body_statement"
	}
	return false
}
