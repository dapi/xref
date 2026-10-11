package xrefgo

import "testing"

func TestControllerActionInLocalParent(t *testing.T) {
	parentPath := "app/controllers/admin/base_controller.rb"
	parentSrc := []byte("module Admin\n class BaseController < ApplicationController\n  def index\n   Item.where(id: 1)\n  end\n end\nend\n")
	childPath := "app/controllers/admin/items_controller.rb"
	childSrc := []byte("module Admin\n class ItemsController < BaseController\n end\nend\n")
	pm, pc, _, _, _ := scanGraph(parentPath, parentSrc)
	cm, cc, _, _, _ := scanGraph(childPath, childSrc)
	parentClasses := scanControllerClasses(parentPath, parentSrc)
	childClasses := scanControllerClasses(childPath, childSrc)
	if len(parentClasses) != 1 || parentClasses[0].Owner != "Admin::BaseController" || parentClasses[0].Superclass != "ApplicationController" || len(childClasses) != 1 || childClasses[0].Superclass != "BaseController" {
		t.Fatalf("classes: %#v %#v", parentClasses, childClasses)
	}
	idx := &Index{Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/admin/items", Controller: "admin/items", Action: "index", Source: "config/routes.rb", Line: 1}}},
		parentPath:         {Methods: pm, Calls: pc, Classes: parentClasses, Refs: []Ref{{Path: parentPath, Line: 4, Kind: "orm_table", Table: "items", Confidence: "candidate"}}},
		childPath:          {Methods: cm, Calls: cc, Classes: childClasses},
	}}
	report := traceReport(idx, "GET /admin/items", true, 10)
	if len(report.Chains) != 1 || len(report.Gaps) != 0 || report.Chains[0].Steps[0].Path != parentPath || report.Chains[0].Steps[0].Name != "Admin::BaseController#index" {
		t.Fatalf("inherited action: %#v", report)
	}
}

func TestControllerActionFromUnindexedSuperclassIsCandidate(t *testing.T) {
	parentPath := "app/controllers/admin/application_controller.rb"
	parentSrc := []byte("module Admin\n class ApplicationController < Administrate::ApplicationController\n end\nend\n")
	childPath := "app/controllers/admin/tariffs_controller.rb"
	childSrc := []byte("module Admin\n class TariffsController < ApplicationController\n end\nend\n")
	idx := &Index{Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/admin/tariffs", Controller: "admin/tariffs", Action: "index", Source: "config/routes.rb", Line: 1}}},
		parentPath:         {Classes: scanControllerClasses(parentPath, parentSrc)},
		childPath:          {Classes: scanControllerClasses(childPath, childSrc)},
	}}
	report := traceReport(idx, "GET /admin/tariffs", true, 10)
	if len(report.Chains) != 0 || len(report.Gaps) != 1 || report.Gaps[0].Kind != "action_in_unindexed_superclass" || report.Gaps[0].Detail != "Administrate::ApplicationController#index" {
		t.Fatalf("unindexed superclass: %#v", report)
	}
}
