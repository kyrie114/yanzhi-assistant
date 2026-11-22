package graph

import "testing"

func TestResolvedLimit(t *testing.T) {
	if resolvedLimit(0) != 300 || resolvedLimit(-1) != 300 || resolvedLimit(12) != 12 {
		t.Fatalf("resolved limit zero=%d negative=%d set=%d", resolvedLimit(0), resolvedLimit(-1), resolvedLimit(12))
	}
}

func TestLimitViewDegreeAndPin(t *testing.T) {
	nodes := []Node{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
		{ID: "c", Name: "C"},
		{ID: "d", Name: "D"},
	}
	edges := []Edge{
		{ID: "ab", Source: "a", Target: "b"},
		{ID: "ac", Source: "a", Target: "c"},
		{ID: "ad", Source: "a", Target: "d", Suspended: true},
		{ID: "bc", Source: "b", Target: "c"},
	}
	got := limitView(nodes, edges, 2, "")
	if !got.Truncated || len(got.Nodes) != 2 || got.Nodes[0].ID != "a" || got.Nodes[1].ID != "b" {
		t.Fatalf("truncated nodes %+v flag %v", got.Nodes, got.Truncated)
	}
	if len(got.Edges) != 1 || got.Edges[0].ID != "ab" {
		t.Fatalf("edges %+v", got.Edges)
	}

	tie := limitView(
		[]Node{{ID: "c", Name: "c"}, {ID: "b", Name: "b"}},
		[]Edge{{ID: "cb", Source: "c", Target: "b"}},
		1,
		"",
	)
	if !tie.Truncated || len(tie.Nodes) != 1 || tie.Nodes[0].ID != "b" {
		t.Fatalf("tie nodes %+v", tie.Nodes)
	}

	suspendedOnly := limitView(
		[]Node{{ID: "a", Name: "a"}, {ID: "m", Name: "m"}, {ID: "z", Name: "z"}},
		[]Edge{
			{ID: "zm", Source: "z", Target: "m", Suspended: true},
			{ID: "za", Source: "z", Target: "a", Suspended: true},
		},
		1,
		"",
	)
	if len(suspendedOnly.Nodes) != 1 || suspendedOnly.Nodes[0].ID != "z" {
		t.Fatalf("suspended degree kept %+v", suspendedOnly.Nodes)
	}

	keptSuspended := limitView(
		[]Node{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}},
		[]Edge{{ID: "ab", Source: "a", Target: "b", Suspended: true}},
		10,
		"",
	)
	if len(keptSuspended.Edges) != 1 || !keptSuspended.Edges[0].Suspended {
		t.Fatalf("suspended edge %+v", keptSuspended.Edges)
	}

	hop := limitView(
		[]Node{
			{ID: "p", Name: "paper"},
			{ID: "n1", Name: "n1"},
			{ID: "n2", Name: "n2"},
			{ID: "n3", Name: "n3"},
		},
		[]Edge{
			{ID: "p1", Source: "p", Target: "n1"},
			{ID: "12", Source: "n1", Target: "n2"},
			{ID: "23", Source: "n2", Target: "n3"},
			{ID: "31", Source: "n3", Target: "n1"},
		},
		2,
		"p",
	)
	if !hop.Truncated || len(hop.Nodes) != 2 || hop.Nodes[0].ID != "p" || hop.Nodes[1].ID != "n1" {
		t.Fatalf("hop nodes %+v truncated %v", hop.Nodes, hop.Truncated)
	}
	if len(hop.Edges) != 1 || hop.Edges[0].ID != "p1" {
		t.Fatalf("hop edges %+v", hop.Edges)
	}

	full := limitView(
		[]Node{{ID: "p", Name: "paper"}, {ID: "n1", Name: "n1"}, {ID: "n2", Name: "n2"}},
		[]Edge{
			{ID: "p1", Source: "p", Target: "n1"},
			{ID: "p2", Source: "p", Target: "n2"},
			{ID: "12", Source: "n1", Target: "n2"},
		},
		10,
		"p",
	)
	if full.Truncated || len(full.Nodes) != 3 || len(full.Edges) != 2 {
		t.Fatalf("full hop %+v", full)
	}
	for _, e := range full.Edges {
		if e.ID == "12" {
			t.Fatal("neighbor edge included")
		}
	}

	empty := limitView(nil, nil, 0, "")
	if empty.Nodes == nil || empty.Edges == nil || empty.Truncated {
		t.Fatalf("empty view %+v", empty)
	}
}
