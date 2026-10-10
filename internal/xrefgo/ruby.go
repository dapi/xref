package xrefgo

import (
	"strconv"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
)

// A leading English verb is not sufficient: UI copy such as "Select product"
// is common in Rails trees. Require the structure of a SQL statement.
func looksSQL(text string) bool {
	s := " " + strings.ToUpper(strings.Join(strings.Fields(text), " ")) + " "
	switch {
	case strings.HasPrefix(s, " SELECT "):
		return strings.Contains(s, " FROM ")
	case strings.HasPrefix(s, " WITH "):
		return strings.Contains(s, " SELECT ") || strings.Contains(s, " UPDATE ") || strings.Contains(s, " DELETE ")
	case strings.HasPrefix(s, " UPDATE "):
		return strings.Contains(s, " SET ")
	case strings.HasPrefix(s, " INSERT "):
		return strings.Contains(s, " INTO ")
	case strings.HasPrefix(s, " DELETE "):
		return strings.Contains(s, " FROM ")
	case strings.HasPrefix(s, " CREATE "), strings.HasPrefix(s, " ALTER "), strings.HasPrefix(s, " DROP "):
		return strings.Contains(s, " TABLE ") || strings.Contains(s, " INDEX ") || strings.Contains(s, " VIEW ")
	}
	return false
}

func rubyTree(src []byte) (*ts.Parser, *ts.Tree, error) {
	p := ts.NewParser()
	if err := p.SetLanguage(ts.NewLanguage(ruby.Language())); err != nil {
		p.Close()
		return nil, nil, err
	}
	t := p.Parse(src, nil)
	return p, t, nil
}

func modelDeclarations(path string, src []byte, schema map[string]bool) map[string]ModelInfo {
	models := map[string]ModelInfo{}
	if !strings.Contains(path, "app/models/") {
		return models
	}
	p, tree, err := rubyTree(src)
	if err != nil {
		return models
	}
	defer p.Close()
	defer tree.Close()
	var walk func(*ts.Node, string)
	walk = func(n *ts.Node, namespace string) {
		if n.Kind() == "module" {
			name := fieldText(n, "name", src)
			if name != "" {
				if strings.Contains(name, "::") {
					namespace = name
				} else if namespace != "" {
					namespace += "::" + name
				} else {
					namespace = name
				}
			}
		}
		if n.Kind() == "class" {
			name := fieldText(n, "name", src)
			if name != "" && namespace != "" && !strings.Contains(name, "::") {
				name = namespace + "::" + name
			}
			base := name
			if pos := strings.LastIndex(name, "::"); pos >= 0 {
				base = name[pos+2:]
			}
			if base != "" {
				table := explicitTableName(n, src)
				explicit := table != ""
				isModel := explicit || strings.Contains(fieldText(n, "superclass", src), "ApplicationRecord") || strings.Contains(fieldText(n, "superclass", src), "ActiveRecord::Base")
				if isModel && table == "" {
					table = tableize(base)
				}
				if isModel {
					info := ModelInfo{Table: table, Explicit: explicit, Verified: schema[table]}
					models[name] = info
					models[base] = info
				}
			}
			if name != "" {
				namespace = name
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), namespace)
		}
	}
	walk(tree.RootNode(), "")
	return models
}

func explicitTableName(class *ts.Node, src []byte) string {
	var found string
	var walk func(*ts.Node)
	walk = func(n *ts.Node) {
		if found != "" || n.Kind() == "class" && !n.Equals(*class) {
			return
		}
		if n.Kind() == "assignment" && fieldText(n, "left", src) == "self.table_name" {
			right := n.ChildByFieldName("right")
			if right != nil && right.Kind() == "string" {
				found = strings.Trim(right.Utf8Text(src), "'\"")
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(class)
	return found
}

func tableize(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	s := strings.ToLower(b.String())
	if strings.HasSuffix(s, "y") && len(s) > 1 && !strings.ContainsRune("aeiou", rune(s[len(s)-2])) {
		return s[:len(s)-1] + "ies"
	}
	if strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") || strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh") {
		return s + "es"
	}
	return s + "s"
}

func scanRuby(path string, src []byte, models map[string]ModelInfo) ([]Ref, int) {
	p, tree, err := rubyTree(src)
	if err != nil {
		return nil, 1
	}
	defer p.Close()
	defer tree.Close()
	refs := []Ref{}
	errors := 0
	if tree.RootNode().HasError() {
		errors++
	}
	var walk func(*ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "string", "heredoc_body":
			text := n.Utf8Text(src)
			interpolated := true
			if n.Kind() == "string" {
				interpolated = !strings.HasPrefix(text, "'")
				text = strings.Trim(text, "'\"")
			}
			if looksSQL(text) {
				line := int(n.StartPosition().Row) + 1
				if interpolated {
					query, holes, unsafe := substituteSQLHoles(text, line)
					appendSQLWithHoles(&refs, path, line, query, holes, unsafe)
				} else {
					appendSQL(&refs, path, line, text)
				}
			}
		case "call":
			method := fieldText(n, "method", src)
			if !ormMethod(method) {
				break
			}
			model := callRoot(n.ChildByFieldName("receiver"), src)
			info := models[model]
			if info.Table == "" {
				break
			}
			table := info.Table
			line := int(n.StartPosition().Row) + 1
			confidence := "candidate"
			if info.Explicit {
				confidence = "fact"
			} else if info.Verified {
				confidence = "inferred"
			}
			refs = append(refs, Ref{Path: path, Line: line, Kind: "orm_table", Table: table, Confidence: confidence, Detail: model + "." + method})
			args := n.ChildByFieldName("arguments")
			if args != nil {
				for _, column := range ormColumns(args, method, src) {
					refs = append(refs, Ref{Path: path, Line: line, Kind: "orm_column", Table: table, Column: column, Confidence: "candidate", Detail: model + "." + method})
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return dedup(refs), errors
}

func fieldText(n *ts.Node, field string, src []byte) string {
	if c := n.ChildByFieldName(field); c != nil {
		return c.Utf8Text(src)
	}
	return ""
}

func callRoot(n *ts.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "constant", "scope_resolution":
		return n.Utf8Text(src)
	case "call":
		return callRoot(n.ChildByFieldName("receiver"), src)
	}
	return ""
}

func ormMethod(s string) bool {
	switch s {
	case "where", "find_by", "find_by!", "order", "reorder", "group", "select", "pluck", "pick", "sum", "average", "minimum", "maximum", "count", "update_all", "delete_all", "joins", "left_joins", "includes", "preload", "exists?":
		return true
	}
	return false
}

func ormColumns(args *ts.Node, method string, src []byte) []string {
	set := map[string]bool{}
	var pairs func(*ts.Node)
	pairs = func(n *ts.Node) {
		if n.Kind() == "pair" {
			key := n.ChildByFieldName("key")
			if key != nil {
				name := strings.TrimPrefix(key.Utf8Text(src), ":")
				if name != "" {
					set[name] = true
				}
			}
			return
		}
		if n.Kind() == "hash" || n.Kind() == "argument_list" {
			for i := uint(0); i < n.NamedChildCount(); i++ {
				pairs(n.NamedChild(i))
			}
		}
	}
	pairs(args)
	if method == "pluck" || method == "pick" || method == "select" || method == "order" || method == "reorder" || method == "group" || method == "sum" || method == "average" || method == "minimum" || method == "maximum" {
		for i := uint(0); i < args.NamedChildCount(); i++ {
			c := args.NamedChild(i)
			if c.Kind() == "simple_symbol" {
				set[strings.TrimPrefix(c.Utf8Text(src), ":")] = true
			}
		}
	}
	return keys(set)
}

func appendSQL(refs *[]Ref, path string, line int, query string) {
	appendSQLWithHoles(refs, path, line, query, nil, false)
}

func appendSQLWithHoles(refs *[]Ref, path string, line int, query string, holes []sqlHole, unsafe bool) {
	for _, h := range holes {
		*refs = append(*refs, Ref{Path: path, Line: h.Line, Offset: h.Offset, Kind: "sql_hole", Confidence: "blind", Detail: h.Context})
	}
	if unsafe {
		*refs = append(*refs, Ref{Path: path, Line: line, Kind: "sql_blind", Confidence: "blind", Detail: "динамическое имя или незакрытая интерполяция SQL"})
		return
	}
	facts, err := parseSQL(query)
	if err != nil {
		*refs = append(*refs, Ref{Path: path, Line: line, Kind: "sql_blind", Confidence: "blind", Detail: "SQL не разобран"})
		return
	}
	kind := "parsed_sql"
	if len(holes) > 0 {
		kind = "parsed_sql_dynamic"
	}
	for _, table := range facts.Tables {
		*refs = append(*refs, Ref{Path: path, Line: line, Kind: kind, Table: table, Confidence: "fact"})
	}
	if len(facts.Tables) == 1 {
		for _, column := range facts.Columns {
			*refs = append(*refs, Ref{Path: path, Line: line, Kind: kind, Table: facts.Tables[0], Column: column, Confidence: "fact"})
		}
	}
	for owner, columns := range facts.Qualified {
		for _, column := range columns {
			*refs = append(*refs, Ref{Path: path, Line: line, Kind: kind, Table: owner, Column: column, Confidence: "fact"})
		}
	}
}

func dedup(refs []Ref) []Ref {
	seen := map[string]bool{}
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		key := r.Path + "|" + r.Kind + "|" + r.Table + "|" + r.Column + "|" + r.Detail + "|" + strconv.Itoa(r.Line) + "|" + strconv.Itoa(r.Offset)
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}
