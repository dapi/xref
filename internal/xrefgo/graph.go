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
func scanGraph(path string, src []byte) ([]Method, []Call, []Route) {
	p, tree, err := rubyTree(src)
	if err != nil {
		return nil, nil, nil
	}
	defer p.Close()
	defer tree.Close()
	methods, calls, routes := []Method{}, []Call{}, []Route{}
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
			}
			source, sourceClass = name, class
		case "call":
			name := fieldText(n, "method", src)
			if source != "" && owner != "" && name != "" {
				receiver := n.ChildByFieldName("receiver")
				value := ""
				if receiver != nil {
					switch receiver.Kind() {
					case "self":
						value = "self"
					case "constant", "scope_resolution":
						value = receiver.Utf8Text(src)
					case "call":
						if fieldText(receiver, "method", src) == "new" {
							value = callRoot(receiver.ChildByFieldName("receiver"), src) + ".new"
						}
					}
				}
				calls = append(calls, Call{Owner: owner, Source: source, SourceClass: sourceClass, Receiver: value, Method: name, Path: path, Line: int(n.StartPosition().Row) + 1})
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
	return methods, calls, dedupRoutes(routes)
}

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
		if name == "root" {
			url = ""
		}
		target := routeArg(args, src, "to")
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
	methods map[string][]Method
	forward map[string][]resolvedCall
	routes  []Route
	refs    map[string][]Ref
	files   map[string]File
}
type resolvedCall struct {
	Call   Call   `json:"call"`
	Target Method `json:"target"`
}

func buildGraph(idx *Index) Graph {
	g := Graph{methods: map[string][]Method{}, forward: map[string][]resolvedCall{}, routes: allRoutes(idx), refs: map[string][]Ref{}, files: idx.Files}
	for _, f := range idx.Files {
		for _, m := range f.Methods {
			k := methodKey(m.Owner, m.Name, m.Class)
			g.methods[k] = append(g.methods[k], m)
		}
	}
	for _, f := range idx.Files {
		for _, r := range f.Refs {
			for _, m := range f.Methods {
				if r.Line >= m.Line && r.Line <= m.EndLine {
					g.refs[methodKey(m.Owner, m.Name, m.Class)] = append(g.refs[methodKey(m.Owner, m.Name, m.Class)], r)
					break
				}
			}
		}
		for _, c := range f.Calls {
			targetOwner, class := c.Owner, c.SourceClass
			targetMethod := c.Method
			switch {
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
				sourceKey := methodKey(c.Owner, c.Source, c.SourceClass)
				g.forward[sourceKey] = append(g.forward[sourceKey], resolvedCall{c, candidates[0]})
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
type TraceReport struct {
	Query     string       `json:"query"`
	Routes    int          `json:"matching_routes"`
	Chains    []TraceChain `json:"chains"`
	Truncated bool         `json:"truncated"`
	Note      string       `json:"note"`
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
	owner := controllerOwner(r.Controller)
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
		for _, edge := range g.forward[k] {
			next := methodKey(edge.Target.Owner, edge.Target.Name, edge.Target.Class)
			visit(next, append(append([]TraceStep(nil), steps...), TraceStep{Kind: "call", Path: edge.Call.Path, Line: edge.Call.Line, Name: edge.Target.Owner + "#" + edge.Target.Name, Confidence: "candidate"}), depth+1)
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

func traceReport(idx *Index, query string, routeMode bool, limit int) TraceReport {
	g := buildGraph(idx)
	report := TraceReport{Query: query, Chains: []TraceChain{}, Note: "Статическая оценка: маршруты, render шаблона и цели вызовов требуют проверки в работающем Rails; отсутствие цепочки не доказывает недостижимость."}
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
