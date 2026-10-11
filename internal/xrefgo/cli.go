package xrefgo

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type schemaFlags map[string]string

func (s schemaFlags) String() string { return fmt.Sprint(map[string]string(s)) }
func (s schemaFlags) Set(v string) error {
	name, path, ok := strings.Cut(v, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("ожидается имя=путь")
	}
	s[name] = path
	return nil
}

func Run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("xref-go", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", ".", "корень проекта")
	indexFlag := fs.String("index", "", "путь к локальному индексу")
	jsonFlag := fs.Bool("json", false, "JSON")
	limit := fs.Int("limit", 50, "число строк в отчёте")
	includeTests := fs.Bool("include-tests", false, "учитывать тестовый код в blind")
	schemas := schemaFlags{}
	fs.Var(schemas, "schema", "источник схемы: имя=путь или имя=pg:ИСТОЧНИК, можно повторять")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, usage())
			return nil
		}
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" {
		fmt.Fprintln(out, usage())
		return nil
	}
	if *limit < 1 {
		return fmt.Errorf("--limit должен быть больше нуля")
	}
	switch rest[0] {
	case "refresh", "where", "find", "blind", "drift", "audit", "coverage", "route", "trace", "callers":
	default:
		return fmt.Errorf("неизвестная команда %q\n%s", rest[0], usage())
	}
	root, err := filepath.Abs(*project)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	path := *indexFlag
	if path == "" {
		path, err = indexPath(root)
		if err != nil {
			return err
		}
	}
	var old *Index
	old, err = loadIndex(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(schemas) == 0 {
		hasLocal := false
		if old != nil {
			for _, s := range old.Schemas {
				if !strings.HasPrefix(s.Path, "pg:") {
					hasLocal = true
					break
				}
			}
		}
		defaultPath := filepath.Join(root, "db/structure.sql")
		if !hasLocal {
			if _, err := os.Stat(defaultPath); err == nil {
				schemas["repo"] = "db/structure.sql"
			}
		}
	}
	idx, changed, err := refresh(root, path, old, schemas)
	if err != nil {
		return err
	}
	cmd := rest[0]
	var result any
	switch cmd {
	case "refresh":
		result = map[string]any{"files": len(idx.Files), "changed": changed, "schemas": schemaSummary(idx), "blind_spots": idx.BlindSpots, "index": path}
	case "where":
		if len(rest) != 2 {
			return fmt.Errorf("where требует table или table.column")
		}
		result = where(idx, rest[1])
	case "find":
		if len(rest) != 2 || rest[1] == "" {
			return fmt.Errorf("find требует текст")
		}
		result, err = find(idx, rest[1], *limit)
		if err != nil {
			return err
		}
	case "blind":
		if len(idx.Schemas) == 0 {
			return fmt.Errorf("blind требует источник схемы: --schema имя=путь")
		}
		result = blind(idx, *includeTests)
	case "drift":
		result = drift(idx)
	case "audit":
		result = audit(idx, changed, path)
	case "coverage":
		if len(rest) != 1 {
			return fmt.Errorf("coverage не принимает аргументы")
		}
		result = coverageReport(idx)
	case "route", "trace":
		if len(rest) != 2 || rest[1] == "" {
			return fmt.Errorf("%s требует путь маршрута или имя таблицы", cmd)
		}
		result = traceReport(idx, rest[1], cmd == "route", *limit)
	case "callers":
		if len(rest) != 2 || rest[1] == "" {
			return fmt.Errorf("callers требует относительный путь файла")
		}
		result = callersReport(idx, filepath.ToSlash(strings.TrimPrefix(rest[1], "./")))
	default:
		return fmt.Errorf("неизвестная команда %q", cmd)
	}
	if *jsonFlag {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	printHuman(out, cmd, result, *limit, idx.BlindSpots)
	return nil
}

func usage() string {
	return "xref-go [--project ПУТЬ] [--schema ИМЯ=ПУТЬ|pg:ИСТОЧНИК] [--json] [--limit N] КОМАНДА\nКоманды: refresh, where <таблица[.колонка]>, find <текст>, blind, drift, audit, coverage, route <[VERB ]/путь>, trace <таблица[.колонка]>, callers <файл>"
}

func allRefs(idx *Index) []Ref {
	out := []Ref{}
	for _, f := range expandedFiles(idx) {
		out = append(out, f.Refs...)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Column < b.Column
	})
	return out
}

func where(idx *Index, query string) []Ref {
	table, col, _ := strings.Cut(query, ".")
	out := []Ref{}
	for _, r := range allRefs(idx) {
		if r.Table != table {
			continue
		}
		if col != "" && r.Column != col {
			continue
		}
		out = append(out, r)
	}
	return out
}

type Hit struct {
	Path       string   `json:"path"`
	Line       int      `json:"line"`
	Text       string   `json:"text"`
	FileTables []string `json:"file_tables,omitempty"`
	Indexed    bool     `json:"indexed"`
}

func find(idx *Index, pattern string, limit int) ([]Hit, error) {
	needle := strings.ToLower(pattern)
	hits := []Hit{}
	paths, err := trackedFiles(idx.Root)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		abs, info, ok := regularWithinRoot(idx.Root, path)
		if !ok || info.Size() > 8*1024*1024 {
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			continue
		}
		seen := map[string]bool{}
		file, indexed := idx.Files[path]
		for _, r := range file.Refs {
			if r.Table != "" {
				seen[r.Table] = true
			}
		}
		tables := keys(seen)
		for n, line := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				if len(hits) < limit {
					hits = append(hits, Hit{Path: path, Line: n + 1, Text: strings.TrimSpace(line), FileTables: tables, Indexed: indexed})
				}
			}
		}
	}
	return hits, nil
}

type Missing struct {
	Source string `json:"source"`
	Table  string `json:"table"`
	Ref    Ref    `json:"ref"`
}

func blind(idx *Index, includeTests bool) []Missing {
	out := []Missing{}
	for name, s := range idx.Schemas {
		for _, r := range allRefs(idx) {
			if r.Table == "" || r.Column != "" {
				continue
			}
			if !includeTests && (strings.HasPrefix(r.Path, "spec/") || strings.HasPrefix(r.Path, "test/") || strings.HasPrefix(r.Path, "features/")) {
				continue
			}
			if strings.HasPrefix(r.Table, "pg_") || strings.HasPrefix(r.Table, "sql_") {
				continue
			}
			if r.Kind != "parsed_sql" && r.Kind != "parsed_sql_dynamic" && r.Kind != "orm_table" {
				continue
			}
			if _, ok := s.Tables[r.Table]; !ok {
				out = append(out, Missing{Source: name, Table: r.Table, Ref: r})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Ref.Path != b.Ref.Path {
			return a.Ref.Path < b.Ref.Path
		}
		return a.Ref.Line < b.Ref.Line
	})
	return out
}

type Difference struct {
	Table   string   `json:"table"`
	Column  string   `json:"column,omitempty"`
	Present []string `json:"present"`
	Missing []string `json:"missing"`
}

func drift(idx *Index) []Difference {
	if len(idx.Schemas) < 2 {
		return nil
	}
	all := map[string]map[string]bool{}
	for name, s := range idx.Schemas {
		for table, cols := range s.Tables {
			if all[table] == nil {
				all[table] = map[string]bool{}
			}
			all[table][name] = true
			for _, col := range cols {
				key := table + "." + col
				if all[key] == nil {
					all[key] = map[string]bool{}
				}
				all[key][name] = true
			}
		}
	}
	names := make([]string, 0, len(idx.Schemas))
	for name := range idx.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []Difference{}
	for key, present := range all {
		if len(present) == len(names) {
			continue
		}
		table, col, _ := strings.Cut(key, ".")
		d := Difference{Table: table, Column: col}
		for _, name := range names {
			if present[name] {
				d.Present = append(d.Present, name)
			} else {
				d.Missing = append(d.Missing, name)
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Column < out[j].Column
	})
	return out
}

func schemaSummary(idx *Index) map[string]int {
	m := map[string]int{}
	for name, s := range idx.Schemas {
		m[name] = len(s.Tables)
	}
	return m
}

func audit(idx *Index, changed int, path string) map[string]any {
	files := expandedFiles(idx)
	refs := 0
	parseErrors := 0
	templateFiles, templateSegments, templateErrors := 0, 0, 0
	byKind := map[string]int{}
	holeContexts := map[string]int{}
	for name, f := range files {
		parseErrors += f.Errors
		if strings.HasSuffix(name, ".erb") || strings.HasSuffix(name, ".haml") {
			templateFiles++
			templateSegments += f.Segments
			templateErrors += f.Errors
		}
		for _, r := range f.Refs {
			refs++
			byKind[r.Kind]++
			if r.Kind == "sql_hole" {
				holeContexts[r.Detail]++
			}
		}
	}
	schemaErrors := map[string]int{}
	for name, s := range idx.Schemas {
		schemaErrors[name] = s.ParseError
	}
	unsupported := map[string]int{}
	if paths, err := trackedFiles(idx.Root); err == nil {
		for _, p := range paths {
			if _, ok := idx.Files[p]; !ok {
				switch filepath.Ext(p) {
				case ".erb", ".haml", ".slim", ".builder", ".jbuilder":
					unsupported[filepath.Ext(p)]++
				}
			}
		}
	}
	methods, calls, routes, renders, redirects, dynamicReceivers := 0, 0, 0, 0, 0, 0
	scopes, localBindings, variableReceivers := 0, 0, 0
	for _, f := range files {
		methods += len(f.Methods)
		localBindings += len(f.Locals)
		for _, m := range f.Methods {
			if isScope(m) {
				scopes++
			}
		}
		calls += len(f.Calls)
		routes += len(f.Routes)
		for _, r := range f.Renders {
			if r.Kind == "redirect" {
				redirects++
			} else {
				renders++
			}
		}
		for _, c := range f.Calls {
			if c.ReceiverKind == "dynamic" {
				dynamicReceivers++
			} else if c.ReceiverKind == "variable" {
				variableReceivers++
			}
		}
	}
	resolvedVariableCalls := 0
	for _, edges := range buildGraph(idx).forward {
		for _, edge := range edges {
			if edge.Call.ReceiverKind == "variable" {
				resolvedVariableCalls++
			}
		}
	}
	return map[string]any{"project": idx.Root, "index": path, "updated": idx.Updated, "indexed_files": len(idx.Files), "changed_files": changed, "references": refs, "reference_kinds": byKind, "sql_hole_contexts": holeContexts, "ruby_parse_errors": parseErrors - templateErrors, "template_delimiter_errors": templateErrors, "template_files": templateFiles, "template_ruby_segments": templateSegments, "sql_blind_spots": idx.BlindSpots, "schemas": schemaSummary(idx), "schema_parse_errors": schemaErrors, "model_mappings": len(idx.ModelMap), "ambiguous_models": len(idx.AmbiguousModels), "unparsed_templates": unsupported, "methods": methods, "scope_declarations": scopes, "calls": calls, "local_bindings": localBindings, "variable_receivers": variableReceivers, "resolved_variable_calls": resolvedVariableCalls, "route_candidates": routes, "render_calls": renders, "redirect_calls": redirects, "dynamic_receivers": dynamicReceivers, "limitations": []string{"Static routes and calls are candidates; Rails runtime routing, filters, dynamic dispatch and redirects are unresolved", "Explicit render links cover literal action, template and partial targets; branches and custom renderers are unresolved", "Variable calls require one direct local assignment of a class constant or Class.new; reassignment and branches remain unresolved", "Rails scopes in models and explicitly included concerns require literal names and inline lambdas; relation chains require known scope or relation methods", "ERB/Haml extraction covers common Ruby tags, script lines, attributes and interpolation but not the full template languages", "Slim and other template formats are not parsed", "repository structure.sql supports CREATE TABLE only; live PostgreSQL sources read public tables", "production logs are not connected"}}
}

func printHuman(out io.Writer, cmd string, v any, limit int, blindSpots int) {
	switch x := v.(type) {
	case []Ref:
		fmt.Fprintf(out, "Найдено: %d\n", len(x))
		if len(x) == 0 && blindSpots > 0 {
			fmt.Fprintf(out, "Индекс содержит %d слепых зон SQL; отсутствие находок не доказывает отсутствие обращения.\n", blindSpots)
		}
		for i, r := range x {
			if i >= limit {
				break
			}
			what := r.Table
			if r.Column != "" {
				what += "." + r.Column
			}
			fmt.Fprintf(out, "%s:%d  %-25s %-20s %s\n", r.Path, r.Line, r.Kind, what, r.Confidence)
		}
	case []Hit:
		fmt.Fprintf(out, "Совпадений (показано не более %d): %d\n", limit, len(x))
		for _, h := range x {
			fmt.Fprintf(out, "%s:%d  %s\n", h.Path, h.Line, h.Text)
		}
	case []Missing:
		fmt.Fprintf(out, "Возможных обращений к отсутствующим таблицам: %d\n", len(x))
		for i, m := range x {
			if i >= limit {
				break
			}
			fmt.Fprintf(out, "%s  %s:%d  %s (%s)\n", m.Source, m.Ref.Path, m.Ref.Line, m.Table, m.Ref.Confidence)
		}
	case []Difference:
		if x == nil {
			fmt.Fprintln(out, "Для сравнения схем нужны хотя бы два источника --schema.")
			return
		}
		fmt.Fprintf(out, "Расхождений: %d\n", len(x))
		for i, d := range x {
			if i >= limit {
				break
			}
			name := d.Table
			if d.Column != "" {
				name += "." + d.Column
			}
			fmt.Fprintf(out, "%s  есть: %s; нет: %s\n", name, strings.Join(d.Present, ","), strings.Join(d.Missing, ","))
		}
	case TraceReport:
		fmt.Fprintf(out, "Цепочек: %d", len(x.Chains))
		if x.Truncated {
			fmt.Fprint(out, " (вывод ограничен)")
		}
		fmt.Fprintln(out)
		for _, chain := range x.Chains {
			fmt.Fprintf(out, "%s %s → %s#%s (%s:%d)\n", chain.Route.Verb, chain.Route.Path, chain.Route.Controller, chain.Route.Action, chain.Route.Source, chain.Route.Line)
			for _, step := range chain.Steps {
				fmt.Fprintf(out, "  → %s %s:%d %s [%s]\n", step.Kind, step.Path, step.Line, step.Name, step.Confidence)
			}
			name := chain.Ref.Table
			if chain.Ref.Column != "" {
				name += "." + chain.Ref.Column
			}
			fmt.Fprintf(out, "  → %s:%d %s [%s]\n", chain.Ref.Path, chain.Ref.Line, name, chain.Ref.Confidence)
		}
		if len(x.Gaps) > 0 {
			fmt.Fprintf(out, "Пробелы трассировки: %d", len(x.Gaps))
			if x.GapsTruncated {
				fmt.Fprint(out, " (вывод ограничен)")
			}
			fmt.Fprintln(out)
			for _, gap := range x.Gaps {
				fmt.Fprintf(out, "  %s:%d %s %s — %s\n", gap.Path, gap.Line, gap.Kind, gap.Detail, gap.Reason)
			}
		}
		fmt.Fprintln(out, x.Note)
	case CoverageReport:
		fmt.Fprintf(out, "Маршрутов: %d; с обращением к таблице: %d; без него: %d; с пробелами: %d\n", x.Routes, x.WithReferences, x.WithoutReferences, x.WithGaps)
		fmt.Fprintf(out, "Цепочек: %d; пробелов по маршрутам: %d; уникальных мест: %d\n", x.TotalChains, x.TotalGaps, x.UniqueGaps)
		kinds := make([]string, 0, len(x.GapKinds))
		for kind := range x.GapKinds {
			kinds = append(kinds, kind)
		}
		sort.Slice(kinds, func(i, j int) bool {
			if x.GapKinds[kinds[i]] != x.GapKinds[kinds[j]] {
				return x.GapKinds[kinds[i]] > x.GapKinds[kinds[j]]
			}
			return kinds[i] < kinds[j]
		})
		for _, kind := range kinds {
			fmt.Fprintf(out, "  %s: %d по маршрутам, %d уникальных мест\n", kind, x.GapKinds[kind], x.UniqueGapKinds[kind])
		}
		fmt.Fprintf(out, "Маршруты (первые %d из %d по приоритету):\n", min(limit, len(x.Rows)), len(x.Rows))
		for i, row := range x.Rows {
			if i >= limit {
				break
			}
			fmt.Fprintf(out, "%s %s → %s#%s (%s:%d): цепочек %d, пробелов %d\n", row.Route.Verb, row.Route.Path, row.Route.Controller, row.Route.Action, row.Route.Source, row.Route.Line, row.Chains, len(row.Gaps))
			for j, gap := range row.Gaps {
				if j >= 3 {
					break
				}
				fmt.Fprintf(out, "  %s:%d %s %s — %s\n", gap.Path, gap.Line, gap.Kind, gap.Detail, gap.Reason)
			}
			if len(row.Gaps) > 3 {
				fmt.Fprintf(out, "  ещё пробелов: %d (полный список: --json coverage)\n", len(row.Gaps)-3)
			}
			if row.FirstReference != nil {
				fmt.Fprintf(out, "  пример: %s:%d %s\n", row.FirstReference.Path, row.FirstReference.Line, row.FirstReference.Table)
			}
		}
		fmt.Fprintln(out, x.Note)
	case CallerReport:
		fmt.Fprintf(out, "%s: методов %d, вызовов %d, маршрутов %d\n", x.File, len(x.Methods), len(x.Calls), len(x.Routes))
		for _, r := range x.Routes {
			fmt.Fprintf(out, "%s %s (%s:%d) [%s]\n", r.Verb, r.Path, r.Source, r.Line, r.Confidence)
		}
		for _, c := range x.Calls {
			fmt.Fprintf(out, "%s:%d %s#%s → %s#%s [candidate]\n", c.Call.Path, c.Call.Line, c.Call.Owner, c.Call.Source, c.Target.Owner, c.Target.Name)
		}
	case map[string]any:
		b, _ := json.MarshalIndent(x, "", "  ")
		fmt.Fprintln(out, string(b))
	default:
		fmt.Fprintf(out, "%v\n", x)
	}
}
