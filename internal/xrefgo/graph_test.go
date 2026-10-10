package xrefgo

import (
	"strings"
	"testing"
)

func TestRouteAndCallTrace(t *testing.T) {
	routePath := "config/routes/operator.rb"
	routeSrc := []byte("scope module: :operator, path: 'operator' do\n  resources :currencies, only: %i[index create] do\n    member do\n      get :history, to: 'currencies#history'\n    end\n    resources :rates, only: [:show]\n  end\n  get 'rates', to: 'currencies#index'\n  get 'legacy' => 'currencies#show'\nend\n")
	_, _, routes := scanGraph(routePath, routeSrc)
	var indexRoute, createRoute, memberRoute, nestedRoute, legacyRoute bool
	for _, r := range routes {
		if r.Path == "/operator/currencies" && r.Controller == "operator/currencies" {
			if r.Verb == "GET" && r.Action == "index" {
				indexRoute = true
			}
			if r.Verb == "POST" && r.Action == "create" {
				createRoute = true
			}
		}
		if r.Path == "/operator/currencies/:id/history" && r.Action == "history" {
			memberRoute = true
		}
		if r.Path == "/operator/currencies/:currency_id/rates/:id" && r.Action == "show" {
			nestedRoute = true
		}
		if r.Path == "/operator/legacy" && r.Action == "show" {
			legacyRoute = true
		}
	}
	if !indexRoute || !createRoute || !memberRoute || !nestedRoute || !legacyRoute {
		t.Fatalf("routes: %#v", routes)
	}
	controllerPath := "app/controllers/operator/currencies_controller.rb"
	controllerSrc := []byte("class Operator::CurrenciesController < ApplicationController\n def create\n  ExchangeRate.add_rate\n end\nend\n")
	modelPath := "app/models/exchange_rate.rb"
	modelSrc := []byte("class ExchangeRate < ApplicationRecord\n def self.add_rate\n  ExchangeRate.where(id: 1)\n end\nend\n")
	cm, cc, _ := scanGraph(controllerPath, controllerSrc)
	mm, _, _ := scanGraph(modelPath, modelSrc)
	if len(cm) != 1 || len(mm) != 1 || len(cc) == 0 {
		t.Fatalf("graph: %#v %#v %#v", cm, mm, cc)
	}
	idx := &Index{Files: map[string]File{
		routePath:      {Routes: routes},
		controllerPath: {Methods: cm, Calls: cc},
		modelPath:      {Methods: mm, Refs: []Ref{{Path: modelPath, Line: 3, Table: "exchange_rates", Kind: "orm_table", Confidence: "candidate"}}},
		"app/views/operator/currencies/index.html.haml": {Refs: []Ref{{Path: "app/views/operator/currencies/index.html.haml", Line: 2, Table: "exchange_rates", Kind: "orm_table", Confidence: "candidate"}}},
	}}
	post := traceReport(idx, "POST /operator/currencies", true, 10)
	if len(post.Chains) != 1 || len(post.Chains[0].Steps) != 2 {
		t.Fatalf("post: %#v", post)
	}
	get := traceReport(idx, "GET /operator/currencies", true, 10)
	if len(get.Chains) != 1 || get.Chains[0].Steps[0].Kind != "template" {
		t.Fatalf("get: %#v", get)
	}
	callers := callersReport(idx, modelPath)
	if len(callers.Calls) != 1 {
		t.Fatalf("callers: %#v", callers)
	}
}

func TestDynamicReceiverDoesNotInventCallEdge(t *testing.T) {
	path := "app/controllers/orders_controller.rb"
	methods, calls, _ := scanGraph(path, []byte("class OrdersController\n def show\n  service.call\n end\nend\n"))
	idx := &Index{Files: map[string]File{path: {Methods: methods, Calls: calls}, "app/services/service.rb": {Methods: []Method{{Owner: "Service", Name: "call", Path: "app/services/service.rb", Line: 1, EndLine: 2}}}}}
	for _, edges := range buildGraph(idx).forward {
		if len(edges) > 0 {
			t.Fatalf("dynamic call resolved: %#v", edges)
		}
	}
	if !strings.Contains(traceReport(idx, "GET /orders", true, 10).Note, "не доказывает") {
		t.Fatal("missing caveat")
	}
}

func TestConstructorReachesInitialize(t *testing.T) {
	controllerPath := "app/controllers/stores_controller.rb"
	servicePath := "app/queries/stores_query.rb"
	cm, cc, _ := scanGraph(controllerPath, []byte("class StoresController\n def index\n  StoresQuery.new.call\n end\nend\n"))
	sm, sc, _ := scanGraph(servicePath, []byte("class StoresQuery\n def initialize\n  Vendor.where(active: true)\n end\n def call\n end\nend\n"))
	idx := &Index{Files: map[string]File{
		"config/routes.rb": {Routes: []Route{{Verb: "GET", Path: "/stores", Controller: "stores", Action: "index"}}},
		controllerPath:     {Methods: cm, Calls: cc},
		servicePath:        {Methods: sm, Calls: sc, Refs: []Ref{{Path: servicePath, Line: 3, Table: "vendors", Kind: "orm_table", Confidence: "candidate"}}},
	}}
	report := traceReport(idx, "GET /stores", true, 10)
	if len(report.Chains) != 1 || report.Chains[0].Steps[len(report.Chains[0].Steps)-1].Name != "StoresQuery#initialize" {
		t.Fatalf("constructor trace: %#v", report)
	}
}
