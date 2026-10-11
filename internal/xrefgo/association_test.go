package xrefgo

import "testing"

func TestAssociationInIncludedConcernReachesRoute(t *testing.T) {
	models := map[string]ModelInfo{
		"Account": {Table: "accounts", Verified: true},
		"Entry":   {Table: "entries", Verified: true},
	}
	accountPath := "app/models/account.rb"
	includeSrc := []byte("class Account < ApplicationRecord\n include AccountLinks\nend\n")
	include := scanModelIncludes(accountPath, includeSrc, models)
	concernPath := "app/models/concerns/account_links.rb"
	concernSrc := []byte("module AccountLinks\n included do\n  has_many :entries, dependent: :destroy\n end\nend\n")
	associations := scanModelAssociations(concernPath, concernSrc, models)
	if len(associations) != 1 || associations[0].Target != "Entry" || len(include) != 1 {
		t.Fatalf("association declarations: %#v %#v", associations, include)
	}
	controllerPath := "app/controllers/account/entries_controller.rb"
	controllerSrc := []byte("class Account::EntriesController\n def index\n  entries.to_a\n end\n private\n def entries\n  current_account.entries.order(:published_at)\n end\nend\n")
	methods, calls, _, _, _ := scanGraph(controllerPath, controllerSrc)
	idx := &Index{ModelMap: models, Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/entries", Controller: "account/entries", Action: "index"}}},
		accountPath:        {Includes: include},
		concernPath:        {Associations: associations},
		controllerPath:     {Methods: methods, Calls: calls},
	}}
	report := traceReport(idx, "GET /entries", true, 20)
	if len(report.Chains) == 0 || report.Chains[0].Ref.Table != "entries" || report.Chains[0].Ref.Kind != "orm_association" || report.Chains[0].Ref.Confidence != "candidate" {
		t.Fatalf("route association: %#v; calls: %#v; refs: %#v; forward: %#v", report, calls, buildGraph(idx).refs, buildGraph(idx).forward)
	}
}

func TestAmbiguousOrUnincludedAssociationDoesNotCreateReference(t *testing.T) {
	models := map[string]ModelInfo{"Account": {Table: "accounts"}, "Entry": {Table: "entries"}, "Memo": {Table: "memos"}}
	path := "app/controllers/account/entries_controller.rb"
	methods, calls, _, _, _ := scanGraph(path, []byte("class Account::EntriesController\n def index\n  current_account.entries.to_a\n end\nend\n"))
	idx := &Index{ModelMap: models, Files: map[string]File{
		path:                              {Methods: methods, Calls: calls},
		"app/models/concerns/relation.rb": {Associations: []ModelAssociation{{Owner: "Relation", Name: "entries", Target: "Entry", Path: "app/models/concerns/relation.rb", Line: 2}}},
	}}
	if refs := buildGraph(idx).refs[methodKey("Account::EntriesController", "index", false)]; len(refs) != 0 {
		t.Fatalf("unincluded concern created reference: %#v", refs)
	}
	idx.Files["app/models/account.rb"] = File{Includes: []ModelInclude{{Owner: "Account", Module: "Relation"}}}
	f := idx.Files["app/models/concerns/relation.rb"]
	f.Associations = append(f.Associations, ModelAssociation{Owner: "Relation", Name: "entries", Target: "Memo", Path: "app/models/concerns/relation.rb", Line: 3})
	idx.Files["app/models/concerns/relation.rb"] = f
	if refs := buildGraph(idx).refs[methodKey("Account::EntriesController", "index", false)]; len(refs) != 0 {
		t.Fatalf("ambiguous association created reference: %#v", refs)
	}
}

func TestDirectModelFindIsCandidateReference(t *testing.T) {
	models := map[string]ModelInfo{"Entry": {Table: "entries", Verified: true}}
	refs, errors := scanRuby("app/controllers/entries_controller.rb", []byte("Entry.find(params[:id])\nEntry.new\n"), models)
	if errors != 0 || len(refs) != 1 || refs[0].Table != "entries" || refs[0].Confidence != "inferred" || refs[0].Detail != "Entry.find" {
		t.Fatalf("model entry points: %#v errors=%d", refs, errors)
	}
}

func TestParameterDoesNotBecomeNoArgMethodOrModelHelper(t *testing.T) {
	models := map[string]ModelInfo{"Account": {Table: "accounts"}, "Entry": {Table: "entries"}}
	path := "app/controllers/account/entries_controller.rb"
	src := []byte("class Account::EntriesController\n def index(entries, current_account)\n  entries.to_a\n  current_account.entries\n end\n def entries\n  Entry.find(1)\n end\nend\n")
	methods, calls, _, _, locals := scanGraph(path, src)
	idx := &Index{ModelMap: models, Files: map[string]File{
		path:                    {Methods: methods, Calls: calls, Locals: locals},
		"app/models/account.rb": {Associations: []ModelAssociation{{Owner: "Account", Name: "entries", Target: "Entry", Path: "app/models/account.rb", Line: 1}}},
	}}
	g := buildGraph(idx)
	key := methodKey("Account::EntriesController", "index", false)
	if len(g.refs[key]) != 0 || len(g.forward[key]) != 0 {
		t.Fatalf("parameters produced false links: refs=%#v calls=%#v", g.refs[key], g.forward[key])
	}
}

func TestAssociationNeedsKnownTargetAndDirectDeclaration(t *testing.T) {
	models := map[string]ModelInfo{"Account": {Table: "accounts"}, "Entry": {Table: "entries"}}
	src := []byte("class Account < ApplicationRecord\n has_many :entries, class_name: relation_class\n has_many :published_posts, through: :entries, class_name: 'Entry'\n def demo\n  has_many :entries\n end\n has_many :posts, class_name: 'Entry'\nend\n")
	associations := scanModelAssociations("app/models/account.rb", src, models)
	if len(associations) != 1 || associations[0].Name != "posts" || associations[0].Target != "Entry" {
		t.Fatalf("unsafe association declarations: %#v", associations)
	}
}
