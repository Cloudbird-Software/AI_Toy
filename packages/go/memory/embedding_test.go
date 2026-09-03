package memory

import (
	"math"
	"testing"
)

func TestStubEmbedderDeterminism(t *testing.T) {
	e := StubEmbedder{}
	v1, err := e.Embed("hello")
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	v2, err := e.Embed("hello")
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(v1) != 384 {
		t.Fatalf("expected 384 dim, got %d", len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("non-deterministic at dim %d: %v vs %v", i, v1[i], v2[i])
		}
	}
}

func TestSearchByEmbeddingSameDomainOnly(t *testing.T) {
	st, err := NewStore(Options{MaxNodes: 10, Embedder: StubEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("u1", Node{Subject: "x", Pred: "likes", Text: "apple"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("u2", Node{Subject: "y", Pred: "likes", Text: "apple"}, nil); err != nil {
		t.Fatal(err)
	}
	res := st.SearchByEmbedding("u1", "apple", 5, 0)
	for _, n := range res {
		if n.UserID != "u1" {
			t.Fatalf("leak to other domain: %s", n.UserID)
		}
	}
}

func TestSearchByEmbeddingTopK(t *testing.T) {
	st, err := NewStore(Options{MaxNodes: 10, Embedder: StubEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		text := string(rune('a' + i))
		if err := st.Write("u1", Node{Subject: "x", Pred: "likes", Text: text}, nil); err != nil {
			t.Fatal(err)
		}
	}
	res := st.SearchByEmbedding("u1", "x", 3, 0)
	if len(res) > 3 {
		t.Fatalf("expected topK=3, got %d", len(res))
	}
}

func TestSearchByEmbeddingNilEmbedder(t *testing.T) {
	st, err := NewStore(Options{MaxNodes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("u1", Node{Subject: "x", Pred: "likes", Text: "apple"}, nil); err != nil {
		t.Fatal(err)
	}
	res := st.SearchByEmbedding("u1", "apple", 5, 0)
	if res != nil {
		t.Fatalf("expected nil with nil embedder, got %d results", len(res))
	}
}

func TestWritePrecomputesEmbedding(t *testing.T) {
	st, err := NewStore(Options{MaxNodes: 10, Embedder: StubEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("u1", Node{Subject: "x", Pred: "likes", Text: "apple"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(st.embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(st.embeddings))
	}
	for _, vec := range st.embeddings {
		if len(vec) != 384 {
			t.Fatalf("expected 384 dim, got %d", len(vec))
		}
		norm := float64(0)
		for _, v := range vec {
			norm += v * v
		}
		norm = math.Sqrt(norm)
		if norm < 5.0 || norm > 30.0 {
			t.Fatalf("embedding norm out of expected range: %v", norm)
		}
	}
}
