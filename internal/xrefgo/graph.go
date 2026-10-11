package xrefgo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// scanGraph records syntax facts. Resolution of calls and Rails routes happens
// separately, so a parsed call never implies that Ruby will dispatch to it.
func scanGraph(path string, src []byte) ([]Method, []Call, []Route, []Render, []LocalBinding) {
	p, tree, err := rubyTree(src)
	if err != nil {
		return nil, nil, nil, nil, nil
	}
	defer p.Close()
	defer tree.Close()
	methods, calls, routes := []Method{}, []Call{}, []Route{}
	renders := []Render{}
	locals := []LocalBinding{}
	var walk func(*ts.Node, string, string, bool, routeScope)
	walk = func(n *ts.Node, owner, source string, sourceClass bool, scope routeScope) {
		switch n.Kind() {
		case "module", "class":
			name := fieldText(n, "name", src)
			if name != "" {
				if owner != "" && !strings.Contains(name, "::") {
					name = owner + "::" + name
				}
				owner = strings.TrimPrefix(name, "::")
			}
		case "method", "singleton_method":
			name := fieldText(n, "name", src)
			class := n.Kind() == "singleton_method"
			if owner != "" && name != "" {
				methods = append(methods, Method{Owner: owner, Name: name, Class: class, Path: path, Line: int(n.StartPosition().Row) + 1, EndLine: int(n.EndPosition().Row) + 1})
				locals = append(locals, methodBindings(path, n, src, owner, name, class)...)
			}
			source, sourceClass = name, class
		case "call":
			name := fieldText(n, "method", src)
			if strings.HasPrefix(path, "app/controllers/") && source != "" && owner != "" {
				if name == "render" {
					if render, ok := renderFromCall(path, src, n, owner, source, sourceClass); ok {
						renders = append(renders, render)
					}
				} else if name == "redirect_to" || name == "redirect_back_or_to" {
					receiver := n.ChildByFieldName("receiver")
					if receiver == nil || receiver.Kind() == "self" {
						renders = append(renders, Render{Owner: owner, Source: source, SourceClass: sourceClass, Path: path, Line: int(n.StartPosition().Row) + 1, Kind: "redirect"})
					}
				}
			}
			if source != "" && owner != "" && name != "" {
				receiver := n.ChildByFieldName("receiver")
				value := ""
				receiverKind := "implicit"
				var prefix []string
				if receiver != nil {
					receiverKind = "dynamic"
					switch receiver.Kind() {
					case "identifier":
						value = receiver.Utf8Text(src)
						receiverKind = "variable"
					case "self":
						value = "self"
						receiverKind = "self"
					case "constant", "scope_resolution":
						value = receiver.Utf8Text(src)
						receiverKind = "constant"
					case "call":
						if fieldText(receiver, "method", src) == "new" {
							if root := callRoot(receiver.ChildByFieldName("receiver"), src); root != "" {
								value = root + ".new"
								receiverKind = "constructed"
							}
						} else if root, chain := relationChain(receiver, src); root != "" && len(chain) > 0 {
							value, prefix = root, chain
							receiverKind = "relation"
						}
					}
				}
				calls = append(calls, Call{Owner: owner, Source: source, SourceClass: sourceClass, Receiver: value, ReceiverKind: receiverKind, Method: name, Path: path, Line: int(n.StartPosition().Row) + 1, Direct: directStatementCall(n), Prefix: prefix})
			}
			if strings.HasPrefix(path, "config/routes") {
				routes = append(routes, routeFromCall(path, src, n, scope)...)
				scope = childRouteScope(src, n, scope)
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			child := n.NamedChild(i)
			if n.Kind() == "call" && child.Kind() == "do_block" {
				walk(child, owner, source, sourceClass, scope)
			} else if n.Kind() == "call" && child.Kind() == "call" {
				// Nested receiver calls belong to the same method, but not to a route scope.
				walk(child, owner, source, sourceClass, routeScope{})
			} else if n.Kind() != "call" || child.Kind() != "argument_list" {
				walk(child, owner, source, sourceClass, scope)
			}
		}
	}
	walk(tree.RootNode(), "", "", false, routeScope{})
	return methods, calls, dedupRoutes(routes), renders, locals
}

func relationChain(n *ts.Node, src []byte) (string, []string) {
	if n == nil {
		return "", nil
	}
	if n.Kind() == "constant" || n.Kind() == "scope_resolution" {
		return strings.TrimPrefix(n.Utf8Text(src), "::"), nil
	}
	if n.Kind() != "call" {
		return "", nil
	}
	root, chain := relationChain(n.ChildByFieldName("receiver"), src)
	if root == "" {
		return "", nil
	}
	name := fieldText(n, "method", src)
	if name == "" || name == "new" {
		return "", nil
	}
	return root, append(chain, name)
}

func relationMethod(name string) bool {
	switch name {
	case "where", "not", "joins", "left_joins", "includes", "preload", "order", "reorder", "group", "select", "merge", "limit", "offset", "distinct", "references":
		return true
	}
	return false
}

// These Rails persistence names can touch a database even when their
// implementation is supplied by a gem and absent from the project index.
func persistenceMethod(name string) bool {
	switch name {
	case "create", "create!", "update", "update!", "update_all", "destroy", "destroy!", "delete", "delete_all", "save", "save!", "reload", "with_lock", "transaction":
		return true
	}
	return false
}

func isScope(m Method) bool { return m.Kind == "scope" || m.Kind == "concern_scope" }

type routeScope struct{ path, module, resource string }

func routeArg(args *ts.Node, src []byte, key string) string {
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c.Kind() == "pair" && strings.TrimSuffix(fieldText(c, "key", src), ":") == key {
			v := c.ChildByFieldName("value")
			if v != nil {
				return unquoteRoute(v.Utf8Text(src))
			}
		}
	}
	return ""
}

func routeFirst(args *ts.Node, src []byte) string {
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c.Kind() != "pair" {
			return unquoteRoute(c.Utf8Text(src))
		}
	}
	return ""
}

func unquoteRoute(s string) string { return strings.Trim(strings.TrimSpace(s), "'\":") }
func singularRoute(s string) string {
	if strings.HasSuffix(s, "ies") {
		return strings.TrimSuffix(s, "ies") + "y"
	}
	if strings.HasSuffix(s, "s") {
		return strings.TrimSuffix(s, "s")
	}
	return s
}
func pluralRoute(s string) string {
	if strings.HasSuffix(s, "s") {
		return s
	}
	if strings.HasSuffix(s, "y") {
		return strings.TrimSuffix(s, "y") + "ies"
	}
	return s + "s"
}
func joinRoute(a, b string) string {
	return "/" + strings.Trim(strings.Trim(a, "/")+"/"+strings.Trim(b, "/"), "/")
}

func childRouteScope(src []byte, n *ts.Node, scope routeScope) routeScope {
	name := fieldText(n, "method", src)
	args := n.ChildByFieldName("arguments")
	switch name {
	case "namespace":
		part := routeFirst(args, src)
		scope.path = joinRoute(scope.path, part)
		scope.module = strings.Trim(strings.Trim(scope.module, "/")+"/"+part, "/")
	case "scope":
		if part := routeArg(args, src, "path"); part != "" {
			scope.path = joinRoute(scope.path, part)
		}
		if part := routeArg(args, src, "module"); part != "" {
			scope.module = strings.Trim(strings.Trim(scope.module, "/")+"/"+part, "/")
		}
	case "resources", "resource":
		part := routeFirst(args, src)
		if part != "" {
			if scope.resource != "" {
				scope.path = joinRoute(scope.path, ":"+singularRoute(scope.resource)+"_id")
			}
			scope.path = joinRoute(scope.path, part)
			scope.resource = part
		}
	case "member":
		if scope.resource != "" {
			scope.path = joinRoute(scope.path, ":id")
		}
	}
	return scope
}

func routeFromCall(path string, src []byte, n *ts.Node, scope routeScope) []Route {
	name := fieldText(n, "method", src)
	args := n.ChildByFieldName("arguments")
	line := int(n.StartPosition().Row) + 1
	makeRoute := func(verb, url, target string) (Route, bool) {
		controller, action, ok := strings.Cut(target, "#")
		if !ok || controller == "" || action == "" {
			return Route{}, false
		}
		if !strings.Contains(controller, "/") && scope.module != "" {
			controller = scope.module + "/" + controller
		}
		return Route{Verb: verb, Path: joinRoute(scope.path, url), Controller: controller, Action: action, Source: path, Line: line, Confidence: "candidate"}, true
	}
	switch name {
	case "get", "post", "put", "patch", "delete", "root":
		url := routeFirst(args, src)
		first := url
		if name == "root" {
			url = ""
		}
		target := routeArg(args, src, "to")
		if name == "root" && target == "" && strings.Contains(first, "#") {
			target = first
		}
		if target == "" && args != nil {
			for i := uint(0); i < args.NamedChildCount(); i++ {
				pair := args.NamedChild(i)
				if pair.Kind() != "pair" {
					continue
				}
				key, val := pair.ChildByFieldName("key"), pair.ChildByFieldName("value")
				if key != nil && val != nil && (key.Kind() == "string" || key.Kind() == "simple_symbol") && strings.Contains(val.Utf8Text(src), "#") {
					url = unquoteRoute(key.Utf8Text(src))
					target = unquoteRoute(val.Utf8Text(src))
					break
				}
			}
		}
		if target == "" && strings.Contains(url, "#") {
			target = url
			url = ""
		}
		if target == "" {
			return nil
		}
		verb := strings.ToUpper(name)
		if name == "root" {
			verb = "GET"
		}
		if r, ok := makeRoute(verb, url, target); ok {
			return []Route{r}
		}
	case "resources", "resource":
		resource := routeFirst(args, src)
		if resource == "" {
			return nil
		}
		controller := routeArg(args, src, "controller")
		if controller == "" {
			controller = resource
			if name == "resource" {
				controller = pluralRoute(resource)
			}
		}
		allowed := map[string]bool{}
		only := routeArg(args, src, "only")
		if only == "" {
			for _, a := range []string{"index", "show", "new", "create", "edit", "update", "destroy"} {
				allowed[a] = true
			}
		} else {
			for _, a := range strings.FieldsFunc(only, func(r rune) bool { return r == '[' || r == ']' || r == ',' || r == ':' || r == ' ' }) {
				allowed[a] = true
			}
		}
		base := scope.path
		if scope.resource != "" {
			base = joinRoute(base, ":"+singularRoute(scope.resource)+"_id")
		}
		base = joinRoute(base, resource)
		if !strings.Contains(controller, "/") && scope.module != "" {
			controller = scope.module + "/" + controller
		}
		items := []struct{ action, verb, suffix string }{{"index", "GET", ""}, {"show", "GET", "/:id"}, {"new", "GET", "/new"}, {"create", "POST", ""}, {"edit", "GET", "/:id/edit"}, {"update", "PATCH", "/:id"}, {"destroy", "DELETE", "/:id"}}
		items = append(items, struct{ action, verb, suffix string }{"update", "PUT", "/:id"})
		if name == "resource" {
			items = []struct{ action, verb, suffix string }{{"show", "GET", ""}, {"new", "GET", "/new"}, {"create", "POST", ""}, {"edit", "GET", "/edit"}, {"update", "PATCH", ""}, {"update", "PUT", ""}, {"destroy", "DELETE", ""}}
		}
		out := []Route{}
		for _, it := range items {
			if allowed[it.action] {
				out = append(out, Route{Verb: it.verb, Path: base + it.suffix, Controller: controller, Action: it.action, Source: path, Line: line, Confidence: "candidate"})
			}
		}
		return out
	}
	return nil
}

func dedupRoutes(in []Route) []Route {
	seen := map[string]bool{}
	out := []Route{}
	for _, r := range in {
		key := fmt.Sprint(r)
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}

func allRoutes(idx *Index) []Route {
	out := []Route{}
	for _, f := range idx.Files {
		out = append(out, f.Routes...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Verb != out[j].Verb {
			return out[i].Verb < out[j].Verb
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func methodKey(owner, name string, class bool) string {
	return fmt.Sprintf("%s|%t|%s", owner, class, name)
}

type Graph struct {
	methods  map[string][]Method
	forward  map[string][]resolvedCall
	renders  map[string][]Render
	callGaps map[string][]TraceGap
	routes   []Route
	refs     map[string][]Ref
	files    map[string]File
	locals   map[string]map[string][]LocalBinding
	classes  map[string][]ClassDecl
}
type resolvedCall struct {
	Call   Call     `json:"call"`
	Target Method   `json:"target"`
	Prefix []Method `json:"prefix,omitempty"`
}

func buildGraph(idx *Index) Graph {
	files := expandedFiles(idx)
	g := Graph{methods: map[string][]Method{}, forward: map[string][]resolvedCall{}, renders: map[string][]Render{}, callGaps: map[string][]TraceGap{}, routes: allRoutes(idx), refs: map[string][]Ref{}, files: files, locals: map[string]map[string][]LocalBinding{}, classes: map[string][]ClassDecl{}}
	owners, names := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		for _, class := range f.Classes {
			g.classes[class.Owner] = append(g.classes[class.Owner], class)
		}
		for _, m := range f.Methods {
			k := methodKey(m.Owner, m.Name, m.Class)
			g.methods[k] = append(g.methods[k], m)
			owners[m.Owner] = true
			names[m.Name] = true
		}
		for _, r := range f.Renders {
			k := methodKey(r.Owner, r.Source, r.SourceClass)
			g.renders[k] = append(g.renders[k], r)
		}
		for _, b := range f.Locals {
			k := methodKey(b.Owner, b.Source, b.SourceClass)
			if g.locals[k] == nil {
				g.locals[k] = map[string][]LocalBinding{}
			}
			g.locals[k][b.Name] = append(g.locals[k][b.Name], b)
		}
	}
	for _, f := range files {
		for _, r := range f.Refs {
			if r.ScopeOwner != "" {
				g.refs[methodKey(r.ScopeOwner, r.ScopeName, true)] = append(g.refs[methodKey(r.ScopeOwner, r.ScopeName, true)], r)
				continue
			}
			for _, m := range f.Methods {
				if r.Line >= m.Line && r.Line <= m.EndLine {
					g.refs[methodKey(m.Owner, m.Name, m.Class)] = append(g.refs[methodKey(m.Owner, m.Name, m.Class)], r)
					break
				}
			}
		}
		for _, c := range f.Calls {
			sourceKey := methodKey(c.Owner, c.Source, c.SourceClass)
			if c.ReceiverKind == "relation" {
				info := idx.ModelMap[c.Receiver]
				prefix := []Method{}
				valid := info.Table != ""
				for _, name := range c.Prefix {
					if relationMethod(name) {
						continue
					}
					candidates := g.methods[methodKey(c.Receiver, name, true)]
					if len(candidates) != 1 || !isScope(candidates[0]) {
						valid = false
						break
					}
					prefix = append(prefix, candidates[0])
				}
				candidates := g.methods[methodKey(c.Receiver, c.Method, true)]
				if valid && len(candidates) == 1 && isScope(candidates[0]) {
					g.forward[sourceKey] = append(g.forward[sourceKey], resolvedCall{Call: c, Target: candidates[0], Prefix: prefix})
				} else if names[c.Method] && !ormMethod(c.Method) || persistenceMethod(c.Method) {
					g.callGaps[sourceKey] = append(g.callGaps[sourceKey], TraceGap{Kind: "dynamic_receiver", Path: c.Path, Line: c.Line, Detail: c.Method, Method: c.Method, Reason: "цепочка relation или scope не разрешена однозначно"})
				}
				continue
			}
			if c.ReceiverKind == "dynamic" {
				if names[c.Method] && !ormMethod(c.Method) || persistenceMethod(c.Method) {
					g.callGaps[sourceKey] = append(g.callGaps[sourceKey], TraceGap{Kind: "dynamic_receiver", Path: c.Path, Line: c.Line, Detail: c.Method, Method: c.Method, Reason: "цель вызова зависит от значения переменной"})
				}
				continue
			}
			var binding LocalBinding
			if c.ReceiverKind == "variable" {
				bindings := g.locals[sourceKey][c.Receiver]
				if len(bindings) != 1 || bindings[0].Target == "" || !c.Direct || bindings[0].Line >= c.Line {
					if names[c.Method] && !ormMethod(c.Method) || persistenceMethod(c.Method) {
						kind := "dynamic_receiver"
						if len(bindings) > 1 {
							kind = "ambiguous_local_binding"
						}
						g.callGaps[sourceKey] = append(g.callGaps[sourceKey], TraceGap{Kind: kind, Path: c.Path, Line: c.Line, Detail: c.Receiver + "." + c.Method, Method: c.Method, Reason: "тип локальной переменной не определён однозначно"})
					}
					continue
				}
				binding = bindings[0]
			}
			targetOwner, class := c.Owner, c.SourceClass
			targetMethod := c.Method
			switch {
			case c.ReceiverKind == "variable":
				targetOwner = binding.Target
				class = binding.Class
			case c.Receiver == "":
			case c.Receiver == "self":
			case strings.HasSuffix(c.Receiver, ".new"):
				targetOwner = strings.TrimSuffix(c.Receiver, ".new")
				class = false
			default:
				targetOwner = c.Receiver
				class = true
			}
			if c.Method == "new" && c.Receiver != "" && c.Receiver != "self" {
				class = false
				targetMethod = "initialize"
			}
			key := methodKey(targetOwner, targetMethod, class)
			candidates := g.methods[key]
			if len(candidates) == 0 && !strings.Contains(targetOwner, "::") && strings.Contains(c.Owner, "::") {
				ns := c.Owner[:strings.LastIndex(c.Owner, "::")]
				candidates = g.methods[methodKey(ns+"::"+targetOwner, targetMethod, class)]
			}
			if len(candidates) == 1 {
				g.forward[sourceKey] = append(g.forward[sourceKey], resolvedCall{Call: c, Target: candidates[0]})
			} else if len(candidates) == 0 && (c.ReceiverKind == "constant" || c.ReceiverKind == "constructed" || c.ReceiverKind == "variable") && c.Method != "new" && !ormMethod(c.Method) && owners[targetOwner] {
				g.callGaps[sourceKey] = append(g.callGaps[sourceKey], TraceGap{Kind: "unresolved_call", Path: c.Path, Line: c.Line, Detail: targetOwner + "#" + c.Method, Reason: "метод не найден в индексе; возможно, он унаследован или определён динамически"})
			}
		}
	}
	for key := range g.forward {
		sort.Slice(g.forward[key], func(i, j int) bool {
			a, b := g.forward[key][i], g.forward[key][j]
			if a.Call.Path != b.Call.Path {
				return a.Call.Path < b.Call.Path
			}
			if a.Call.Line != b.Call.Line {
				return a.Call.Line < b.Call.Line
			}
			return a.Target.Path < b.Target.Path
		})
	}
	return g
}

type TraceStep struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Name       string `json:"name"`
	Confidence string `json:"confidence"`
}
type TraceChain struct {
	Route Route       `json:"route"`
	Steps []TraceStep `json:"steps"`
	Ref   Ref         `json:"ref"`
}
type TraceGap struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Detail string `json:"detail,omitempty"`
	Method string `json:"method,omitempty"`
	Reason string `json:"reason"`
}
type TraceReport struct {
	Query         string       `json:"query"`
	Routes        int          `json:"matching_routes"`
	Chains        []TraceChain `json:"chains"`
	Truncated     bool         `json:"truncated"`
	Gaps          []TraceGap   `json:"gaps,omitempty"`
	GapsTruncated bool         `json:"gaps_truncated,omitempty"`
	Note          string       `json:"note"`
}

func controllerOwner(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = camelRoute(p)
	}
	return strings.Join(parts, "::") + "Controller"
}
func camelRoute(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func routeTemplate(r Route, path string) bool {
	if !strings.HasPrefix(path, "app/views/"+r.Controller+"/"+r.Action+".") {
		return false
	}
	ext := filepath.Ext(path)
	return ext == ".haml" || ext == ".erb"
}

// Find an action in the controller or a locally indexed superclass. An
// unindexed superclass is only a candidate explanation, never a resolved edge.
func (g Graph) controllerAction(owner, action string) (string, string) {
	seen := map[string]bool{}
	for owner != "" && !seen[owner] {
		seen[owner] = true
		if len(g.methods[methodKey(owner, action, false)]) > 0 {
			return owner, ""
		}
		decls := g.classes[owner]
		if len(decls) == 0 {
			return "", ""
		}
		parent := decls[0].Superclass
		for _, decl := range decls[1:] {
			if decl.Superclass != parent {
				return "", ""
			}
		}
		if parent == "" {
			return "", ""
		}
		candidate := parent
		if !strings.Contains(parent, "::") && strings.Contains(owner, "::") {
			ns := owner[:strings.LastIndex(owner, "::")]
			if len(g.classes[ns+"::"+parent]) > 0 {
				candidate = ns + "::" + parent
			}
		}
		if len(g.classes[candidate]) == 0 {
			return "", parent
		}
		owner = candidate
	}
	return "", ""
}

func externalActionCandidate(parent string) bool {
	if parent == "" {
		return false
	}
	switch parent {
	case "ActionController::Base", "ActionController::API", "ActionController::Metal", "AbstractController::Base":
		return false
	}
	return true
}

func (g Graph) routeChains(r Route, query string, limit int) []TraceChain {
	out := []TraceChain{}
	add := func(steps []TraceStep, ref Ref) {
		if query != "" && ref.Table != query && ref.Table+"."+ref.Column != query {
			return
		}
		if len(out) < limit {
			out = append(out, TraceChain{Route: r, Steps: append([]TraceStep(nil), steps...), Ref: ref})
		}
	}
	paths := make([]string, 0, len(g.files))
	for path := range g.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		f := g.files[path]
		if routeTemplate(r, path) {
			for _, ref := range f.Refs {
				add([]TraceStep{{Kind: "template", Path: path, Line: ref.Line, Name: r.Action, Confidence: "candidate"}}, ref)
			}
		}
	}
	owner, _ := g.controllerAction(controllerOwner(r.Controller), r.Action)
	key := methodKey(owner, r.Action, false)
	visited := map[string]bool{}
	var visit func(string, []TraceStep, int)
	visit = func(k string, steps []TraceStep, depth int) {
		if depth > 6 || visited[k] || len(out) >= limit {
			return
		}
		visited[k] = true
		defer delete(visited, k)
		for _, ref := range g.refs[k] {
			add(steps, ref)
		}
		for _, render := range g.renders[k] {
			prefix := renderViewPath(r.Controller, render)
			for _, ref := range viewRefs(g.files, prefix) {
				next := append(append([]TraceStep(nil), steps...), TraceStep{Kind: "render", Path: render.Path, Line: render.Line, Name: render.Target, Confidence: "candidate"}, TraceStep{Kind: "template", Path: ref.Path, Line: ref.Line, Name: render.Target, Confidence: "candidate"})
				add(next, ref)
			}
		}
		for _, edge := range g.forward[k] {
			next := methodKey(edge.Target.Owner, edge.Target.Name, edge.Target.Class)
			chain := append([]TraceStep(nil), steps...)
			for _, method := range edge.Prefix {
				chain = append(chain, TraceStep{Kind: "call", Path: edge.Call.Path, Line: edge.Call.Line, Name: method.Owner + "#" + method.Name, Confidence: "candidate"})
			}
			visit(next, append(chain, TraceStep{Kind: "call", Path: edge.Call.Path, Line: edge.Call.Line, Name: edge.Target.Owner + "#" + edge.Target.Name, Confidence: "candidate"}), depth+1)
		}
	}
	for _, m := range g.methods[key] {
		visit(key, []TraceStep{{Kind: "action", Path: m.Path, Line: m.Line, Name: owner + "#" + r.Action, Confidence: "candidate"}}, 0)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.Path != out[j].Ref.Path {
			return out[i].Ref.Path < out[j].Ref.Path
		}
		return out[i].Ref.Line < out[j].Ref.Line
	})
	return out
}

func (g Graph) routeGaps(r Route, limit int) []TraceGap {
	out := []TraceGap{}
	seen := map[string]bool{}
	add := func(gap TraceGap) {
		key := fmt.Sprintf("%s|%s|%d|%s", gap.Kind, gap.Path, gap.Line, gap.Detail)
		if !seen[key] && len(out) < limit {
			seen[key] = true
			out = append(out, gap)
		}
	}
	requestedOwner := controllerOwner(r.Controller)
	owner, missingParent := g.controllerAction(requestedOwner, r.Action)
	key := methodKey(owner, r.Action, false)
	if owner == "" {
		found := false
		prefix := "app/views/" + r.Controller + "/" + r.Action + "."
		for path := range g.files {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		if !found {
			gap := TraceGap{Kind: "action_not_indexed", Path: r.Source, Line: r.Line, Detail: requestedOwner + "#" + r.Action, Reason: "нет метода действия или обычного шаблона в индексе; действие может быть унаследовано"}
			if externalActionCandidate(missingParent) {
				gap.Kind = "action_in_unindexed_superclass"
				gap.Detail = missingParent + "#" + r.Action
				gap.Reason = "родительский класс отсутствует в локальном индексе; действие может быть предоставлено зависимостью"
			}
			add(gap)
		}
	}
	visited := map[string]bool{}
	var visit func(string, int)
	visit = func(k string, depth int) {
		if depth > 6 || visited[k] || len(out) >= limit {
			return
		}
		visited[k] = true
		defer delete(visited, k)
		for _, gap := range g.callGaps[k] {
			add(gap)
		}
		for _, render := range g.renders[k] {
			switch render.Kind {
			case "dynamic", "file":
				add(TraceGap{Kind: "render_target_unresolved", Path: render.Path, Line: render.Line, Detail: render.Target, Reason: "цель render нельзя связать с шаблоном по исходному коду"})
			case "response":
				add(TraceGap{Kind: "non_template_response", Path: render.Path, Line: render.Line, Reason: "render формирует ответ без шаблона"})
			case "redirect":
				add(TraceGap{Kind: "redirect_not_followed", Path: render.Path, Line: render.Line, Reason: "переход через redirect не прослеживается"})
			case "action", "template", "partial":
				prefix := renderViewPath(r.Controller, render)
				found := false
				for path := range g.files {
					if strings.HasPrefix(path, prefix+".") {
						found = true
						break
					}
				}
				if !found {
					add(TraceGap{Kind: "template_not_indexed", Path: render.Path, Line: render.Line, Detail: render.Target, Reason: "явно указанный шаблон не найден в индексе"})
				} else if len(viewRefs(g.files, prefix)) == 0 {
					add(TraceGap{Kind: "template_without_reference", Path: render.Path, Line: render.Line, Detail: render.Target, Reason: "шаблон найден, но в нём нет распознанных обращений к таблице"})
				}
			}
		}
		for _, edge := range g.forward[k] {
			visit(methodKey(edge.Target.Owner, edge.Target.Name, edge.Target.Class), depth+1)
		}
	}
	visit(key, 0)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func traceReport(idx *Index, query string, routeMode bool, limit int) TraceReport {
	g := buildGraph(idx)
	report := TraceReport{Query: query, Chains: []TraceChain{}, Note: "Статическая оценка по файлам проекта: условия маршрутов, render и цели вызовов могут зависеть от выполнения Ruby; отсутствие цепочки не доказывает недостижимость."}
	verb, path, _ := strings.Cut(query, " ")
	for _, r := range g.routes {
		if routeMode && !(r.Path == query || (path != "" && r.Path == path && strings.EqualFold(r.Verb, verb))) {
			continue
		}
		if routeMode {
			report.Routes++
		}
		filter := query
		if routeMode {
			filter = ""
		}
		chains := g.routeChains(r, filter, limit-len(report.Chains)+1)
		if routeMode {
			gaps := g.routeGaps(r, limit-len(report.Gaps)+1)
			if len(chains) == 0 && len(gaps) == 0 {
				gaps = append(gaps, TraceGap{Kind: "no_indexed_reference", Path: r.Source, Line: r.Line, Detail: r.Controller + "#" + r.Action, Reason: "в прослеженных методах и шаблонах не найдено обращения к таблице"})
			}
			for _, gap := range gaps {
				if len(report.Gaps) >= limit {
					report.GapsTruncated = true
					break
				}
				report.Gaps = append(report.Gaps, gap)
			}
		}
		for _, chain := range chains {
			if len(report.Chains) >= limit {
				report.Truncated = true
				break
			}
			report.Chains = append(report.Chains, chain)
		}
		if report.Truncated {
			break
		}
	}
	return report
}

type CallerReport struct {
	File    string         `json:"file"`
	Methods []Method       `json:"methods"`
	Calls   []resolvedCall `json:"calls"`
	Routes  []Route        `json:"routes"`
}

func callersReport(idx *Index, path string) CallerReport {
	g := buildGraph(idx)
	out := CallerReport{File: path, Methods: []Method{}, Calls: []resolvedCall{}, Routes: []Route{}}
	if f, ok := idx.Files[path]; ok {
		out.Methods = append(out.Methods, f.Methods...)
	}
	for _, edges := range g.forward {
		for _, edge := range edges {
			if edge.Target.Path == path {
				out.Calls = append(out.Calls, edge)
			}
		}
	}
	for _, r := range g.routes {
		for _, m := range out.Methods {
			if m.Owner == controllerOwner(r.Controller) && m.Name == r.Action {
				out.Routes = append(out.Routes, r)
				break
			}
		}
		if routeTemplate(r, path) {
			out.Routes = append(out.Routes, r)
		}
	}
	sort.Slice(out.Calls, func(i, j int) bool {
		if out.Calls[i].Call.Path != out.Calls[j].Call.Path {
			return out.Calls[i].Call.Path < out.Calls[j].Call.Path
		}
		return out.Calls[i].Call.Line < out.Calls[j].Call.Line
	})
	return out
}
