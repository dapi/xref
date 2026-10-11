package xrefgo

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSchemaAndSQLFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structure.sql")
	sql := "CREATE TABLE public.products (id bigint NOT NULL, name text);\nCREATE TABLE public.orders (id bigint, product_id bigint);\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := parseSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 2 || !slices.Equal(s.Tables["products"], []string{"id", "name"}) {
		t.Fatalf("schema: %#v", s.Tables)
	}
	facts, err := parseSQL("SELECT products.name FROM products JOIN orders ON orders.product_id = products.id")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(facts.Tables, "products") || !slices.Contains(facts.Tables, "orders") || !slices.Contains(facts.Qualified["products"], "name") {
		t.Fatalf("facts: %#v", facts)
	}
	aliased, err := parseSQL("SELECT p.id FROM products p")
	if err != nil || !slices.Contains(aliased.Qualified["products"], "id") {
		t.Fatalf("alias: %#v %v", aliased, err)
	}
}

func TestRouteDoesNotReconnectPersistedLiveSchema(t *testing.T) {
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "db/structure.sql"), []byte("CREATE TABLE public.orders (id bigint);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	path := filepath.Join(t.TempDir(), "index.json")
	old := &Index{Version: indexVersion, Root: root, Files: map[string]File{}, Schemas: map[string]Schema{"stage": {Path: "pg:TEST"}}, ModelMap: map[string]ModelInfo{}}
	if err := saveIndex(path, old); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XREF_TEST_PGHOST", "")
	if err := Run([]string{"--project", root, "--index", path, "route", "GET /orders"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := loadIndex(path)
	if err != nil || got == nil {
		t.Fatalf("offline index: %v", err)
	}
	if len(got.Schemas) != 1 || len(got.Schemas["repo"].Tables["orders"]) != 1 {
		t.Fatalf("offline schema fallback: %#v", got.Schemas)
	}
}

func TestRubyReferencesKeepEvidenceLevels(t *testing.T) {
	schema := map[string]bool{"products": true, "people": true}
	models := modelDeclarations("app/models/product.rb", []byte("class Product < ApplicationRecord\nend\n"), schema)
	for k, v := range modelDeclarations("app/models/person.rb", []byte("class Person < ApplicationRecord\n self.table_name = 'people'\nend\n"), schema) {
		models[k] = v
	}
	if models["Product"].Table != "products" || models["Person"].Table != "people" || !models["Person"].Explicit {
		t.Fatalf("models: %#v", models)
	}
	src := []byte("Product.where(id: 1).pluck(:name)\nexecute(\"SELECT products.id FROM products WHERE products.id = 1\")\nlabel = 'Select product'\n")
	refs, errors := scanRuby("app/services/demo.rb", src, models)
	if errors != 0 {
		t.Fatalf("parse errors: %d", errors)
	}
	var orm, sql, blind bool
	for _, r := range refs {
		if r.Kind == "orm_column" && r.Table == "products" && r.Column == "id" && r.Confidence == "candidate" {
			orm = true
		}
		if r.Kind == "parsed_sql" && r.Table == "products" && r.Column == "id" && r.Confidence == "fact" {
			sql = true
		}
		if r.Kind == "sql_blind" {
			blind = true
		}
	}
	if !orm || !sql || blind {
		t.Fatalf("bad refs: %#v", refs)
	}
}

func TestSQLInterpolationDoesNotInventTables(t *testing.T) {
	query, holes, unsafe := substituteSQLHoles("SELECT * FROM shard_#{n}", 10)
	if query != "SELECT * FROM shard_$1" || !unsafe || len(holes) != 1 || holes[0].Context != "glue" {
		t.Fatalf("glued interpolation: %q %#v unsafe=%v", query, holes, unsafe)
	}
	refs, errors := scanRuby("app/demo.rb", []byte("execute(\"SELECT * FROM shard_#{n}\")\n"), nil)
	if errors != 0 {
		t.Fatalf("Ruby parse errors: %d", errors)
	}
	for _, ref := range refs {
		if ref.Table != "" {
			t.Fatalf("invented table: %#v", refs)
		}
	}
	if !hasRefKind(refs, "sql_blind") || !hasRefKind(refs, "sql_hole") {
		t.Fatalf("missing blind spot: %#v", refs)
	}

	refs, errors = scanRuby("app/demo.rb", []byte("execute(\"SELECT products.id FROM products WHERE products.id = #{id}\")\n"), nil)
	if errors != 0 || !hasRefTable(refs, "products") || hasRefKind(refs, "sql_blind") {
		t.Fatalf("value interpolation: %#v errors=%d", refs, errors)
	}
	if len(refs) == 0 || !hasRefKind(refs, "sql_hole") {
		t.Fatalf("missing value hole: %#v", refs)
	}

	query, holes, unsafe = substituteSQLHoles("SELECT id FROM products WHERE name LIKE '%#{term}%'\n AND id IN (#{ids.map { |x| x.id }.join(',')})", 20)
	if unsafe || len(holes) != 2 || holes[0].Context != "quoted" || holes[1].Line != 21 || holes[0].Offset == holes[1].Offset {
		t.Fatalf("multi-hole interpolation: %q %#v unsafe=%v", query, holes, unsafe)
	}
}

func hasRefKind(refs []Ref, kind string) bool {
	for _, r := range refs {
		if r.Kind == kind {
			return true
		}
	}
	return false
}

func hasRefTable(refs []Ref, table string) bool {
	for _, r := range refs {
		if r.Table == table {
			return true
		}
	}
	return false
}

func TestBlindExcludesTestsAndCatalogs(t *testing.T) {
	idx := &Index{Schemas: map[string]Schema{"repo": {Tables: map[string][]string{"products": {"id"}}}}, Files: map[string]File{
		"app/x.rb":       {Refs: []Ref{{Path: "app/x.rb", Line: 1, Kind: "parsed_sql", Table: "missing", Confidence: "fact"}, {Path: "app/x.rb", Line: 2, Kind: "parsed_sql", Table: "pg_trigger", Confidence: "fact"}}},
		"spec/x_spec.rb": {Refs: []Ref{{Path: "spec/x_spec.rb", Line: 1, Kind: "parsed_sql", Table: "fixture_missing", Confidence: "fact"}}},
	}}
	got := blind(idx, false)
	if len(got) != 1 || got[0].Table != "missing" {
		t.Fatalf("blind: %#v", got)
	}
	if len(blind(idx, true)) != 2 {
		t.Fatalf("include tests: %#v", blind(idx, true))
	}
}

func TestDriftKeepsSourceProvenance(t *testing.T) {
	idx := &Index{Schemas: map[string]Schema{
		"stage": {Tables: map[string][]string{"products": {"id", "name"}, "orders": {"id"}}},
		"prod":  {Tables: map[string][]string{"products": {"id"}}},
	}}
	diffs := drift(idx)
	if len(diffs) != 3 {
		t.Fatalf("drift: %#v", diffs)
	}
	var sawColumn, sawTable bool
	for _, d := range diffs {
		if d.Table == "products" && d.Column == "name" && slices.Equal(d.Missing, []string{"prod"}) {
			sawColumn = true
		}
		if d.Table == "orders" && d.Column == "" && slices.Equal(d.Present, []string{"stage"}) {
			sawTable = true
		}
	}
	if !sawColumn || !sawTable {
		t.Fatalf("drift: %#v", diffs)
	}
}

func TestLiveSchemaUsesReadOnlyCatalogAndFeedsDrift(t *testing.T) {
	tools := t.TempDir()
	psql := filepath.Join(tools, "psql")
	script := "#!/bin/sh\n" +
		"[ \"$PGDATABASE\" = xref_test ] || exit 10\n" +
		"[ \"$PGPASSWORD\" = test_password ] || exit 11\n" +
		"case \"$*\" in *BEGIN\\ READ\\ ONLY*) ;; *) exit 12;; esac\n" +
		"case \"$*\" in *test_password*) exit 13;; esac\n" +
		"printf '%s\\n' '{\"products\":[\"id\",\"name\"],\"orders\":[\"id\"]}'\n"
	if err := os.WriteFile(psql, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XREF_TEST_PGHOST", "127.0.0.1")
	t.Setenv("XREF_TEST_PGUSER", "xref")
	t.Setenv("XREF_TEST_PGDATABASE", "xref_test")
	t.Setenv("XREF_TEST_PGPASSWORD", "test_password")
	live, err := parseLiveSchema("TEST")
	if err != nil {
		t.Fatal(err)
	}
	if live.Path != "pg:TEST" || !slices.Equal(live.Tables["products"], []string{"id", "name"}) {
		t.Fatalf("live schema: %#v", live)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "db/structure.sql"), []byte("CREATE TABLE public.products (id bigint);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	idx, _, err := refresh(root, filepath.Join(t.TempDir(), "index.json"), nil, map[string]string{"repo": "db/structure.sql", "live": "pg:TEST"})
	if err != nil {
		t.Fatal(err)
	}
	if len(drift(idx)) != 3 {
		t.Fatalf("drift: %#v", drift(idx))
	}
	t.Setenv("XREF_TEST_PGHOST", "")
	offline, _, err := refresh(root, filepath.Join(t.TempDir(), "offline.json"), idx, map[string]string{})
	if err != nil || len(offline.Schemas) != 1 || len(offline.Schemas["repo"].Tables["products"]) != 1 {
		t.Fatalf("implicit live reconnection: schemas=%d err=%v", len(offline.Schemas), err)
	}
	if _, _, err := refresh(root, filepath.Join(t.TempDir(), "missing.json"), idx, map[string]string{"repo": "db/structure.sql", "live": "pg:TEST"}); err == nil {
		t.Fatal("explicit live connection failure was ignored")
	}
	if _, err := parseLiveSchema("test;evil"); err == nil {
		t.Fatal("accepted invalid source name")
	}
}

func TestRefreshDetectsChangedRubyAndSearchesTemplates(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"app/models", "app/views", "db"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"db/structure.sql": "CREATE TABLE public.products (id bigint, name text);\n", "app/models/product.rb": "class Product < ApplicationRecord\nend\n", "app/views/example.erb": "Find me in a template\n"}
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	idxPath := filepath.Join(t.TempDir(), "index.json")
	idx, n, err := refresh(root, idxPath, nil, map[string]string{"repo": "db/structure.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("first changed=%d", n)
	}
	if info, err := os.Stat(idxPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("index permissions: %#v %v", info, err)
	}
	second, n, err := refresh(root, idxPath, idx, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unchanged changed=%d", n)
	}
	if err := os.WriteFile(filepath.Join(root, "app/models/product.rb"), []byte("class Product < ApplicationRecord\n def self.find_one\n  Product.where(id: 1)\n end\nend\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third, n, err := refresh(root, idxPath, second, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(where(third, "products.id")) == 0 {
		t.Fatalf("changed=%d refs=%#v", n, where(third, "products.id"))
	}
	hits, err := find(third, "Find me", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !hits[0].Indexed || !strings.HasSuffix(hits[0].Path, "example.erb") {
		t.Fatalf("hits: %#v", hits)
	}
	if err := os.WriteFile(filepath.Join(root, "db/structure.sql"), []byte("CREATE TABLE public.products (id bigint, name text, enabled boolean);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fourth, n, err := refresh(root, idxPath, third, map[string]string{})
	if err != nil || n == 0 || len(fourth.Schemas["repo"].Tables["products"]) != 3 {
		t.Fatalf("schema refresh: changed=%d schema=%#v err=%v", n, fourth.Schemas["repo"], err)
	}
}

func TestTemplateRubyReferencesAndSourceLines(t *testing.T) {
	models := map[string]ModelInfo{"Product": {Table: "products", Verified: true}}
	erb := []byte("<%%= Product.where(id: 0) %>\n<%# Product.where(id: 0) %>\n<p><%= Product.where(\n  id: 1\n) %></p>\n")
	refs, errors, segments := scanTemplate("app/views/show.html.erb", erb, models)
	if errors != 0 || segments != 1 || len(where(&Index{Files: map[string]File{"x": {Refs: refs}}}, "products.id")) != 1 {
		t.Fatalf("ERB refs=%#v errors=%d segments=%d", refs, errors, segments)
	}
	for _, r := range refs {
		if r.Line != 3 {
			t.Fatalf("ERB line: %#v", r)
		}
	}
	haml := []byte("-# = Product.where(id: 0)\n%p literal Product.where(id: 0)\n= Product.where(\n  id: 2\n)\n%div{data: Product.where(id: 3)}\nText #{Product.where(id: 4)}\n%span= Product.where(id: 5)\n%div{title: 'x'}= Product.where(id: 6)\n%p Text #{Product.where(id: 7)}\n")
	refs, errors, segments = scanTemplate("app/views/show.html.haml", haml, models)
	if errors != 0 || segments != 7 {
		t.Fatalf("Haml refs=%#v errors=%d segments=%d", refs, errors, segments)
	}
	lines := map[int]bool{}
	for _, r := range where(&Index{Files: map[string]File{"x": {Refs: refs}}}, "products.id") {
		lines[r.Line] = true
	}
	if len(lines) != 6 || !lines[3] || !lines[6] || !lines[7] || !lines[8] || !lines[9] || !lines[10] {
		t.Fatalf("Haml lines: %#v, refs=%#v", lines, refs)
	}
}

func TestNamespaceCollisionsAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app/models"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"db/structure.sql":    "CREATE TABLE public.alpha_items (id bigint);\nCREATE TABLE public.beta_items (id bigint);\n",
		"app/models/alpha.rb": "module Alpha\n class Item < ApplicationRecord\n  self.table_name = 'alpha_items'\n end\nend\n",
		"app/models/beta.rb":  "module Beta\n class Item < ApplicationRecord\n  self.table_name = 'beta_items'\n end\nend\n",
	}
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "private.rb")
	if err := os.WriteFile(secret, []byte("Secret.where(id: 1)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "app/models/secret.rb")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	idx, _, err := refresh(root, filepath.Join(t.TempDir(), "index.json"), nil, map[string]string{"repo": "db/structure.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if idx.ModelMap["Alpha::Item"].Table != "alpha_items" || idx.ModelMap["Beta::Item"].Table != "beta_items" {
		t.Fatalf("qualified models: %#v", idx.ModelMap)
	}
	if _, ok := idx.ModelMap["Item"]; ok || !slices.Contains(idx.AmbiguousModels, "Item") {
		t.Fatalf("ambiguous: %#v / %#v", idx.ModelMap, idx.AmbiguousModels)
	}
	if _, ok := idx.Files["app/models/secret.rb"]; ok {
		t.Fatal("external symlink was indexed")
	}
}
