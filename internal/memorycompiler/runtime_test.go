package memorycompiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartTurnEmptyStateDoesNotInjectIR(t *testing.T) {
	rt := New(t.TempDir())
	ctx, turn := rt.StartTurn(context.Background(), "fix a bug", nil)
	if ctx != "" {
		t.Fatalf("empty compiler state injected context:\n%s", ctx)
	}
	if turn == nil {
		t.Fatal("StartTurn returned nil turn")
	}
	turn.Finish(nil)
}

func TestFailureTraceCreatesConstraintIR(t *testing.T) {
	dir := t.TempDir()
	rt := New(dir)
	_, turn := rt.StartTurn(context.Background(), "fix a bug", nil)
	turn.RecordToolResults([]ToolRecord{
		{Name: "bash", Error: "exit status 1"},
		{Name: "bash", Error: "exit status 1"},
	})
	turn.Finish(nil)

	ctx, _ := rt.StartTurn(context.Background(), "continue", nil)
	if !strings.Contains(ctx, "<memory-compiler-ir>") {
		t.Fatalf("expected learned IR context, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "avoid repeating bash") {
		t.Fatalf("expected repeated-error constraint, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "bugfix-reproduce-first") {
		t.Fatalf("expected matching strategy in IR, got:\n%s", ctx)
	}

	st := readState(t, dir)
	if len(st.Learnings) != 1 {
		t.Fatalf("learnings = %d, want 1", len(st.Learnings))
	}
	if len(st.Mutations) == 0 {
		t.Fatal("expected structured compiler mutations")
	}
	if len(st.NoisyRefs) == 0 {
		t.Fatal("expected noisy memory pattern to be tracked")
	}
	if st.ExecutionState.CurrentPhase != "needs_followup" {
		t.Fatalf("phase = %q, want needs_followup", st.ExecutionState.CurrentPhase)
	}
	if len(st.ExecutionState.ActiveConstraints) == 0 {
		t.Fatal("expected active constraints from learning")
	}
	assertNode(t, st.Nodes, func(n MemoryNode) bool {
		return n.Type == "tool_result" && n.TruthLocked && n.Constraint != nil
	}, "truth-locked failed tool result")
	assertNode(t, st.Nodes, func(n MemoryNode) bool {
		return n.Quality == QualityCorrupted && strings.HasPrefix(n.ID, "noise:")
	}, "corrupted noise node")
	assertEdge(t, st.Edges, "contradicts")
}

func TestSuccessTraceFeedsReusableStrategyAndGraph(t *testing.T) {
	dir := t.TempDir()
	rt := New(dir)
	_, turn := rt.StartTurn(context.Background(), "frontend ui setting", nil)
	turn.RecordToolResults([]ToolRecord{{Name: "go test", Output: "ok"}})
	turn.Finish(nil)

	ctx, _ := rt.StartTurn(context.Background(), "frontend ui setting", nil)
	if !strings.Contains(ctx, "frontend-visual-verify") {
		t.Fatalf("expected learned frontend strategy, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "go test succeeded") {
		t.Fatalf("expected tool-result memory reference, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "verify-ui") {
		t.Fatalf("expected strategy execution plan, got:\n%s", ctx)
	}

	st := readState(t, dir)
	assertNode(t, st.Nodes, func(n MemoryNode) bool {
		return n.Type == "decision" && strings.Contains(n.Content, "frontend-visual-verify")
	}, "decision node for selected strategy")
	assertEdge(t, st.Edges, "derived_from")
}

func TestGraphTraversalFiltersCorruptedMemoryAndExpandsConnectedNodes(t *testing.T) {
	now := time.Now().UTC()
	st := state{
		Nodes: []MemoryNode{
			{
				ID:         "seed",
				Type:       "tool_result",
				Content:    "validated source result",
				Timestamp:  now,
				Confidence: 0.9,
				Quality:    QualityHighSignal,
			},
			{
				ID:         "connected",
				Type:       "fact",
				Content:    "connected supporting constraint",
				Timestamp:  now,
				Confidence: 0.8,
				Quality:    QualityMediumSignal,
				Constraint: &Constraint{Type: "must_use", Text: "use connected graph evidence", Source: "connected"},
			},
			{
				ID:         "corrupted",
				Type:       "fact",
				Content:    "must never appear",
				Timestamp:  now,
				Confidence: 1,
				Quality:    QualityCorrupted,
				Constraint: &Constraint{Type: "must_use", Text: "bad constraint", Source: "corrupted"},
			},
		},
		Edges: []MemoryEdge{{From: "seed", To: "connected", Relation: "supports"}},
	}

	ir := buildIR("fix a bug", st)
	got, err := json.Marshal(ir)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "use connected graph evidence") {
		t.Fatalf("expected connected graph constraint, got:\n%s", text)
	}
	if strings.Contains(text, "must never appear") || strings.Contains(text, "bad constraint") {
		t.Fatalf("corrupted memory leaked into IR:\n%s", text)
	}
}

func TestTruthLockedNodeCannotBeOverwritten(t *testing.T) {
	now := time.Now().UTC()
	nodes := []MemoryNode{{
		ID:          "tool:trace:0",
		Type:        "tool_result",
		Content:     "original result",
		Timestamp:   now,
		Confidence:  0.95,
		Quality:     QualityHighSignal,
		TruthLocked: true,
	}}

	nodes = upsertNode(nodes, MemoryNode{
		ID:         "tool:trace:0",
		Type:       "tool_result",
		Content:    "overwritten result",
		Timestamp:  now.Add(time.Second),
		Confidence: 0.1,
		Quality:    QualityNoise,
	})
	if nodes[0].Content != "original result" {
		t.Fatalf("truth-locked node was overwritten: %+v", nodes[0])
	}
}

func readState(t *testing.T, dir string) state {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func assertNode(t *testing.T, nodes []MemoryNode, pred func(MemoryNode) bool, desc string) {
	t.Helper()
	for _, n := range nodes {
		if pred(n) {
			return
		}
	}
	t.Fatalf("missing node: %s\nnodes=%+v", desc, nodes)
}

func assertEdge(t *testing.T, edges []MemoryEdge, relation string) {
	t.Helper()
	for _, e := range edges {
		if e.Relation == relation {
			return
		}
	}
	t.Fatalf("missing %s edge: %+v", relation, edges)
}
