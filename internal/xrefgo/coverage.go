package xrefgo

import (
	"math"
	"sort"
)

// Coverage counts only indexed references with a known table. A route can have
// both references and gaps; neither proves what Rails executes at runtime.
type RouteCoverage struct {
	Route           Route      `json:"route"`
	Chains          int        `json:"chains"`
	Gaps            []TraceGap `json:"gaps"`
	UnverifiedCalls []TraceGap `json:"unverified_dynamic_calls,omitempty"`
	FirstReference  *Ref       `json:"first_reference,omitempty"`
}

type CoverageReport struct {
	Routes                int             `json:"routes"`
	WithReferences        int             `json:"with_references"`
	WithoutReferences     int             `json:"without_references"`
	WithGaps              int             `json:"with_gaps"`
	TotalChains           int             `json:"total_chains"`
	TotalGaps             int             `json:"total_gaps"`
	UniqueGaps            int             `json:"unique_gaps"`
	GapKinds              map[string]int  `json:"gap_kinds"`
	UniqueGapKinds        map[string]int  `json:"unique_gap_kinds"`
	UnverifiedCalls       int             `json:"unverified_dynamic_calls"`
	UniqueUnverifiedCalls int             `json:"unique_unverified_dynamic_calls"`
	Rows                  []RouteCoverage `json:"rows"`
	Note                  string          `json:"note"`
}

func coverageReport(idx *Index) CoverageReport {
	g := buildGraph(idx)
	out := CoverageReport{
		Routes: len(g.routes), GapKinds: map[string]int{}, UniqueGapKinds: map[string]int{}, Rows: []RouteCoverage{},
		Note: "Статическая оценка кандидатов маршрутов по локальному индексу. Динамический вызов остаётся пробелом, если одноимённый индексированный метод ведёт к таблице или имя соответствует операции записи; остальные динамические вызовы перечислены отдельно и тоже могут обращаться к БД во время работы Rails. Отсутствие цепочки не доказывает недостижимость.",
	}
	type gapLocation struct {
		Kind, Path, Detail string
		Line               int
	}
	seenLocations := map[gapLocation]bool{}
	seenUnverified := map[gapLocation]bool{}
	dataNames := g.dataMethodNames()
	for _, route := range g.routes {
		row := RouteCoverage{Route: route, Gaps: []TraceGap{}}
		for _, gap := range g.routeGaps(route, math.MaxInt) {
			if gap.Kind == "dynamic_receiver" && gap.Method != "" && !dataNames[gap.Method] && !persistenceMethod(gap.Method) {
				row.UnverifiedCalls = append(row.UnverifiedCalls, gap)
			} else {
				row.Gaps = append(row.Gaps, gap)
			}
		}
		for _, chain := range g.routeChains(route, "", math.MaxInt) {
			if chain.Ref.Table == "" {
				continue
			}
			row.Chains++
			if row.FirstReference == nil {
				ref := chain.Ref
				row.FirstReference = &ref
			}
		}
		if row.Chains == 0 && len(row.Gaps) == 0 {
			row.Gaps = append(row.Gaps, TraceGap{Kind: "no_indexed_reference", Path: route.Source, Line: route.Line, Detail: route.Controller + "#" + route.Action, Reason: "в прослеженных методах и шаблонах не найдено обращения к таблице"})
		}
		if row.Chains > 0 {
			out.WithReferences++
		} else {
			out.WithoutReferences++
		}
		if len(row.Gaps) > 0 {
			out.WithGaps++
		}
		out.TotalChains += row.Chains
		out.TotalGaps += len(row.Gaps)
		out.UnverifiedCalls += len(row.UnverifiedCalls)
		for _, call := range row.UnverifiedCalls {
			location := gapLocation{call.Kind, call.Path, call.Detail, call.Line}
			if !seenUnverified[location] {
				seenUnverified[location] = true
				out.UniqueUnverifiedCalls++
			}
		}
		for _, gap := range row.Gaps {
			out.GapKinds[gap.Kind]++
			location := gapLocation{gap.Kind, gap.Path, gap.Detail, gap.Line}
			if !seenLocations[location] {
				seenLocations[location] = true
				out.UniqueGaps++
				out.UniqueGapKinds[gap.Kind]++
			}
		}
		out.Rows = append(out.Rows, row)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		pa, pb := coveragePriority(a), coveragePriority(b)
		if pa != pb {
			return pa < pb
		}
		if len(a.Gaps) != len(b.Gaps) {
			return len(a.Gaps) > len(b.Gaps)
		}
		if a.Route.Path != b.Route.Path {
			return a.Route.Path < b.Route.Path
		}
		if a.Route.Verb != b.Route.Verb {
			return a.Route.Verb < b.Route.Verb
		}
		if a.Route.Source != b.Route.Source {
			return a.Route.Source < b.Route.Source
		}
		return a.Route.Line < b.Route.Line
	})
	return out
}

// A name is a possible data transition only when an indexed method with that
// name reaches a table reference through resolved calls. Matching by name is
// deliberately a candidate signal; receiver type still needs runtime proof.
func (g Graph) dataMethodNames() map[string]bool {
	reverse := map[string][]string{}
	for source, edges := range g.forward {
		for _, edge := range edges {
			target := methodKey(edge.Target.Owner, edge.Target.Name, edge.Target.Class)
			reverse[target] = append(reverse[target], source)
		}
	}
	data := map[string]bool{}
	queue := []string{}
	for key, refs := range g.refs {
		for _, ref := range refs {
			if ref.Table != "" {
				data[key] = true
				queue = append(queue, key)
				break
			}
		}
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, source := range reverse[key] {
			if !data[source] {
				data[source] = true
				queue = append(queue, source)
			}
		}
	}
	names := map[string]bool{}
	for key := range data {
		for _, method := range g.methods[key] {
			names[method.Name] = true
		}
	}
	return names
}

func coveragePriority(row RouteCoverage) int {
	if row.Chains == 0 {
		return 0
	}
	if len(row.Gaps) > 0 {
		return 1
	}
	return 2
}
