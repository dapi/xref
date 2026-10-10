package xrefgo

import "testing"

func TestScopeAndLocalAssignmentTrace(t *testing.T) {
	models := map[string]ModelInfo{"Product": {Table: "products", Verified: true}}
	modelPath := "app/models/product.rb"
	sm, refs := scanRailsScopes(modelPath, []byte("class Product < ApplicationRecord\n scope :common, -> { where product_union_id: nil }\nend\n"), models)
	if len(sm) != 1 || sm[0].Kind != "scope" || len(refs) != 2 || refs[1].Column != "product_union_id" {
		t.Fatalf("scope: %#v %#v", sm, refs)
	}
	controllerPath := "app/controllers/items_controller.rb"
	cm, cc, _, _, locals := scanGraph(controllerPath, []byte("class ItemsController\n def index\n  query = ItemsQuery.new\n  query.call\n end\nend\n"))
	if len(locals) != 1 || locals[0].Target != "ItemsQuery" || len(cc) != 2 {
		t.Fatalf("locals: %#v calls: %#v", locals, cc)
	}
	queryPath := "app/queries/items_query.rb"
	qm, qc, _, _, _ := scanGraph(queryPath, []byte("class ItemsQuery\n def call\n  Product.common\n end\nend\n"))
	idx := &Index{Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/items", Controller: "items", Action: "index"}}},
		controllerPath:     {Methods: cm, Calls: cc, Locals: locals},
		queryPath:          {Methods: qm, Calls: qc},
		modelPath:          {Methods: sm, Refs: refs},
	}}
	report := traceReport(idx, "GET /items", true, 10)
	if len(report.Chains) != 2 {
		t.Fatalf("chains: %#v gaps: %#v", report.Chains, report.Gaps)
	}
	for _, chain := range report.Chains {
		if len(chain.Steps) != 3 || chain.Steps[2].Name != "Product#common" {
			t.Fatalf("steps: %#v", chain.Steps)
		}
	}
}

func TestReassignedLocalStaysUnresolved(t *testing.T) {
	path := "app/controllers/items_controller.rb"
	methods, calls, _, _, locals := scanGraph(path, []byte("class ItemsController\n def index\n  query = ItemsQuery.new\n  if flag\n   query = OtherQuery.new\n  end\n  query.call\n end\nend\n"))
	qm, _, _, _, _ := scanGraph("app/queries/items_query.rb", []byte("class ItemsQuery\n def call\n end\nend\n"))
	idx := &Index{Files: map[string]File{
		"config/routes.rb":           {Routes: []Route{{Verb: "GET", Path: "/items", Controller: "items", Action: "index"}}},
		path:                         {Methods: methods, Calls: calls, Locals: locals},
		"app/queries/items_query.rb": {Methods: qm},
	}}
	report := traceReport(idx, "GET /items", true, 10)
	for _, chain := range report.Chains {
		for _, step := range chain.Steps {
			if step.Name == "ItemsQuery#call" {
				t.Fatalf("invented edge: %#v", chain)
			}
		}
	}
	found := false
	for _, gap := range report.Gaps {
		if gap.Kind == "ambiguous_local_binding" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing ambiguity: %#v", report.Gaps)
	}
}
