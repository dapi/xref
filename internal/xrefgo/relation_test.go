package xrefgo

import "testing"

func TestConcernScopeRelationChain(t *testing.T) {
	models := map[string]ModelInfo{"Item": {Table: "items", Verified: true}}
	modelPath := "app/models/item.rb"
	modelSrc := []byte("class Item < ApplicationRecord\n include PublicationState\n include DisplayOrder\n scope :common, -> { where archived: false }\n scope :active, -> { where enabled: true }\nend\n")
	modelMethods, modelRefs := scanRailsScopes(modelPath, modelSrc, models)
	includes := scanModelIncludes(modelPath, modelSrc, models)
	if len(includes) != 2 {
		t.Fatalf("includes: %#v", includes)
	}
	statePath := "app/models/concerns/publication_state.rb"
	stateScopes := scanConcernScopes(statePath, []byte("module PublicationState\n extend ActiveSupport::Concern\n included do\n  scope :published, -> { active.where visible: true }\n  scope :from_input, ->(active) { active.where excluded: true }\n end\nend\n"))
	orderPath := "app/models/concerns/display_order.rb"
	orderScopes := scanConcernScopes(orderPath, []byte("module DisplayOrder\n extend ActiveSupport::Concern\n included do\n  scope :sorted, -> { where priority: true }\n end\nend\n"))
	if len(stateScopes) != 2 || len(orderScopes) != 1 {
		t.Fatalf("concern scopes: %#v %#v", stateScopes, orderScopes)
	}
	controllerPath := "app/controllers/items_controller.rb"
	cm, cc, _, _, _ := scanGraph(controllerPath, []byte("class ItemsController\n def index\n  CatalogQuery.call\n end\nend\n"))
	queryPath := "app/queries/catalog_query.rb"
	qm, qc, _, _, _ := scanGraph(queryPath, []byte("class CatalogQuery\n def self.call\n  Item.common.published.sorted\n end\nend\n"))
	idx := &Index{ModelMap: models, Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/items", Controller: "items", Action: "index"}}},
		modelPath:          {Methods: modelMethods, Refs: modelRefs, Includes: includes},
		statePath:          {ConcernScopes: stateScopes},
		orderPath:          {ConcernScopes: orderScopes},
		controllerPath:     {Methods: cm, Calls: cc},
		queryPath:          {Methods: qm, Calls: qc},
	}}
	report := traceReport(idx, "GET /items", true, 20)
	if len(report.Gaps) != 0 {
		t.Fatalf("unexpected gaps: %#v", report.Gaps)
	}
	got := map[string][]string{}
	for _, chain := range report.Chains {
		if chain.Ref.Column == "" {
			continue
		}
		for _, step := range chain.Steps {
			got[chain.Ref.Column] = append(got[chain.Ref.Column], step.Name)
		}
	}
	for column, wantLast := range map[string]string{"archived": "Item#common", "visible": "Item#published", "priority": "Item#sorted"} {
		steps := got[column]
		if len(steps) == 0 || steps[len(steps)-1] != wantLast {
			t.Fatalf("%s: steps=%#v, chains=%#v", column, steps, report.Chains)
		}
	}
	if steps := got["priority"]; len(steps) < 3 || steps[len(steps)-3] != "Item#common" || steps[len(steps)-2] != "Item#published" {
		t.Fatalf("relation order: %#v", steps)
	}
	if refs := where(idx, "items.visible"); len(refs) != 1 || refs[0].ScopeOwner != "Item" || refs[0].Path != statePath {
		t.Fatalf("concern where: %#v", refs)
	}
	if refs := where(idx, "items.excluded"); len(refs) != 0 {
		t.Fatalf("lambda parameter mistaken for model relation: %#v", refs)
	}
}

func TestConcernScopeNeedsIncludeAndKnownRelation(t *testing.T) {
	models := map[string]ModelInfo{"Item": {Table: "items", Verified: true}}
	path := "app/models/concerns/publication_state.rb"
	scopes := scanConcernScopes(path, []byte("module PublicationState\n included do\n  scope :published, -> { where visible: true }\n end\nend\n"))
	qm, qc, _, _, _ := scanGraph("app/queries/catalog_query.rb", []byte("class CatalogQuery\n def self.call\n  Item.unknown.published\n end\nend\n"))
	idx := &Index{ModelMap: models, Files: map[string]File{
		"app/models/item.rb":           {},
		path:                           {ConcernScopes: scopes},
		"app/queries/catalog_query.rb": {Methods: qm, Calls: qc},
	}}
	if refs := where(idx, "items.visible"); len(refs) != 0 {
		t.Fatalf("unincluded concern refs: %#v", refs)
	}
	idx.Files["app/models/item.rb"] = File{Includes: []ModelInclude{{Owner: "Item", Module: "PublicationState"}}}
	g := buildGraph(idx)
	if len(g.forward[methodKey("CatalogQuery", "call", true)]) != 0 {
		t.Fatalf("unknown predecessor resolved: %#v", g.forward)
	}
}
