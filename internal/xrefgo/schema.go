package xrefgo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// parseSchema reads CREATE TABLE statements from pg_dump's structure.sql format.
// Unreadable statements are counted; no empty schema is silently called complete.
func parseSchema(path string) (Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Schema{}, err
	}
	s := Schema{Path: path, SHA256: digest(b), Tables: map[string][]string{}}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var stmt strings.Builder
	inside := false
	for scanner.Scan() {
		line := scanner.Text()
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !inside && (strings.HasPrefix(upper, "CREATE TABLE ") || strings.HasPrefix(upper, "CREATE UNLOGGED TABLE ")) {
			inside = true
		}
		if !inside {
			continue
		}
		stmt.WriteString(line)
		stmt.WriteByte('\n')
		if !strings.HasSuffix(strings.TrimSpace(line), ");") {
			continue
		}
		if err := addCreateTable(s.Tables, stmt.String()); err != nil {
			s.ParseError++
		}
		stmt.Reset()
		inside = false
	}
	if err := scanner.Err(); err != nil {
		return s, err
	}
	if inside {
		s.ParseError++
	}
	if len(s.Tables) == 0 {
		return s, fmt.Errorf("в %s не найдено ни одной разобранной CREATE TABLE", path)
	}
	return s, nil
}

func addCreateTable(tables map[string][]string, sql string) error {
	parsed, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return err
	}
	var doc struct {
		Stmts []struct {
			Stmt struct {
				CreateStmt struct {
					Relation struct {
						Relname string `json:"relname"`
					} `json:"relation"`
					TableElts []struct {
						ColumnDef struct {
							Colname string `json:"colname"`
						} `json:"ColumnDef"`
					} `json:"tableElts"`
				} `json:"CreateStmt"`
			} `json:"stmt"`
		} `json:"stmts"`
	}
	if err := json.Unmarshal([]byte(parsed), &doc); err != nil {
		return err
	}
	if len(doc.Stmts) == 0 || doc.Stmts[0].Stmt.CreateStmt.Relation.Relname == "" {
		return fmt.Errorf("not a CREATE TABLE")
	}
	create := doc.Stmts[0].Stmt.CreateStmt
	cols := make([]string, 0, len(create.TableElts))
	for _, e := range create.TableElts {
		if e.ColumnDef.Colname != "" {
			cols = append(cols, e.ColumnDef.Colname)
		}
	}
	tables[create.Relation.Relname] = cols
	return nil
}

type sqlFacts struct {
	Tables    []string
	Columns   []string
	Qualified map[string][]string
}

func parseSQL(query string) (sqlFacts, error) {
	parsed, err := pg_query.ParseToJSON(query)
	if err != nil {
		return sqlFacts{}, err
	}
	var doc any
	if err := json.Unmarshal([]byte(parsed), &doc); err != nil {
		return sqlFacts{}, err
	}
	tables := map[string]bool{}
	columns := map[string]bool{}
	qualified := map[string]map[string]bool{}
	aliases := map[string]string{}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if x, ok := t["RangeVar"].(map[string]any); ok {
				if name, ok := x["relname"].(string); ok {
					tables[name] = true
					if a, ok := x["alias"].(map[string]any); ok {
						if alias, ok := a["aliasname"].(string); ok {
							aliases[alias] = name
						}
					}
				}
			}
			if x, ok := t["ColumnRef"].(map[string]any); ok {
				if fields, ok := x["fields"].([]any); ok {
					parts := []string{}
					for _, f := range fields {
						if m, ok := f.(map[string]any); ok {
							if z, ok := m["String"].(map[string]any); ok {
								if name, ok := z["sval"].(string); ok {
									parts = append(parts, name)
								}
							}
						}
					}
					if len(parts) > 0 {
						columns[parts[len(parts)-1]] = true
					}
					if len(parts) >= 2 {
						owner := parts[len(parts)-2]
						if qualified[owner] == nil {
							qualified[owner] = map[string]bool{}
						}
						qualified[owner][parts[len(parts)-1]] = true
					}
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(doc)
	out := sqlFacts{Tables: keys(tables), Columns: keys(columns), Qualified: map[string][]string{}}
	for k, v := range qualified {
		owner := k
		if table, ok := aliases[k]; ok {
			owner = table
		}
		out.Qualified[owner] = append(out.Qualified[owner], keys(v)...)
	}
	return out, nil
}
