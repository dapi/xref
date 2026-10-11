package xrefgo

import (
	"bytes"
	"strings"
	"testing"
)

func TestCoverageCountsRoutesAndUniqueGapLocations(t *testing.T) {
	path := "app/controllers/records_controller.rb"
	methods, calls, _, _, _ := scanGraph(path, []byte("class RecordsController\n def index\n  other.do_stuff\n  other.fetch\n  other.create!\n  Product.where(id: 1)\n end\n def empty\n end\nend\n"))
	routes := []Route{
		{Verb: "GET", Path: "/found", Controller: "records", Action: "index", Source: "config/routes.rb", Line: 1},
		{Verb: "POST", Path: "/found", Controller: "records", Action: "index", Source: "config/routes.rb", Line: 2},
		{Verb: "GET", Path: "/missing", Controller: "records", Action: "missing", Source: "config/routes.rb", Line: 3},
		{Verb: "GET", Path: "/empty", Controller: "records", Action: "empty", Source: "config/routes.rb", Line: 4},
	}
	idx := &Index{Files: map[string]File{
		"config/routes.rb":      {Routes: routes},
		path:                    {Methods: methods, Calls: calls, Refs: []Ref{{Path: path, Line: 6, Kind: "orm_table", Table: "products", Confidence: "candidate"}, {Path: path, Line: 6, Kind: "sql_hole", Confidence: "blind"}}},
		"app/services/other.rb": {Methods: []Method{{Owner: "Other", Name: "do_stuff", Path: "app/services/other.rb", Line: 1, EndLine: 2}, {Owner: "Other", Name: "fetch", Path: "app/services/other.rb", Line: 3, EndLine: 5}}, Refs: []Ref{{Path: "app/services/other.rb", Line: 4, Kind: "orm_table", Table: "products", Confidence: "candidate"}}},
	}}
	report := coverageReport(idx)
	if report.Routes != 4 || report.WithReferences != 2 || report.WithoutReferences != 2 || report.WithGaps != 4 || report.TotalChains != 2 || report.TotalGaps != 6 || report.UniqueGaps != 4 || report.UnverifiedCalls != 2 || report.UniqueUnverifiedCalls != 1 {
		t.Fatalf("summary: %#v", report)
	}
	if report.GapKinds["dynamic_receiver"] != 4 || report.UniqueGapKinds["dynamic_receiver"] != 2 || report.GapKinds["action_not_indexed"] != 1 || report.GapKinds["no_indexed_reference"] != 1 {
		t.Fatalf("gap kinds: %#v %#v", report.GapKinds, report.UniqueGapKinds)
	}
	for _, row := range report.Rows {
		if row.Route.Path == "/found" && (row.Chains != 1 || row.FirstReference == nil || row.FirstReference.Path != path || row.FirstReference.Line != 6 || len(row.UnverifiedCalls) != 1) {
			t.Fatalf("found route: %#v", row)
		}
	}
	var human bytes.Buffer
	printHuman(&human, "coverage", report, 1, 0)
	if !strings.Contains(human.String(), "уникальных мест: 4") || !strings.Contains(human.String(), "первые 1 из 4") || strings.Count(human.String(), "→ records#") != 1 {
		t.Fatalf("human report: %s", human.String())
	}
}
