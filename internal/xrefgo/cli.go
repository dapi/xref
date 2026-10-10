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
	fs.Var(schemas, "schema", "источник схемы: имя=путь, можно повторять")
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
	case "refresh", "where", "find", "blind", "drift", "audit":
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
	if old == nil && len(schemas) == 0 {
		defaultPath := filepath.Join(root, "db/structure.sql")
		if _, err := os.Stat(defaultPath); err == nil {
			schemas["repo"] = "db/structure.sql"
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
	return "xref-go [--project ПУТЬ] [--schema ИМЯ=ПУТЬ] [--json] [--limit N] КОМАНДА\nКоманды: refresh, where <таблица[.колонка]>, find <текст>, blind, drift, audit"
}

func allRefs(idx *Index) []Ref {
	out := []Ref{}
	for _, f := range idx.Files {
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
	refs := 0
	parseErrors := 0
	templateFiles, templateSegments, templateErrors := 0, 0, 0
	byKind := map[string]int{}
	for name, f := range idx.Files {
		parseErrors += f.Errors
		if strings.HasSuffix(name, ".erb") || strings.HasSuffix(name, ".haml") {
			templateFiles++
			templateSegments += f.Segments
			templateErrors += f.Errors
		}
		for _, r := range f.Refs {
			refs++
			byKind[r.Kind]++
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
	return map[string]any{"project": idx.Root, "index": path, "updated": idx.Updated, "indexed_files": len(idx.Files), "changed_files": changed, "references": refs, "reference_kinds": byKind, "ruby_parse_errors": parseErrors - templateErrors, "template_delimiter_errors": templateErrors, "template_files": templateFiles, "template_ruby_segments": templateSegments, "sql_blind_spots": idx.BlindSpots, "schemas": schemaSummary(idx), "schema_parse_errors": schemaErrors, "model_mappings": len(idx.ModelMap), "ambiguous_models": len(idx.AmbiguousModels), "unparsed_templates": unsupported, "limitations": []string{"Ruby dynamic dispatch and variable-held models are unresolved", "ERB/Haml extraction covers common Ruby tags, script lines, attributes and interpolation but not the full template languages", "Slim and other template formats are not parsed", "only CREATE TABLE statements in pg_dump structure.sql are indexed", "live databases and production logs are not connected"}}
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
	case map[string]any:
		b, _ := json.MarshalIndent(x, "", "  ")
		fmt.Fprintln(out, string(b))
	default:
		fmt.Fprintf(out, "%v\n", x)
	}
}
