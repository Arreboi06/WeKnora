package types

import (
	"database/sql/driver"
	"encoding/json"
	"testing"
)

func TestReferencesValueReturnsJSONStringForDatabaseJSONColumns(t *testing.T) {
	refs := References{&SearchResult{ID: "r1", KnowledgeID: "k1", KnowledgeBaseID: "kb1", ChunkIndex: 2}}
	value, err := refs.Value()
	if err != nil {
		t.Fatalf("References.Value() error = %v", err)
	}
	if _, ok := value.(driver.Value); !ok {
		t.Fatalf("References.Value() returned non-driver value %T", value)
	}
	raw, ok := value.(string)
	if !ok {
		t.Fatalf("References.Value() type = %T, want string", value)
	}
	var decoded []SearchResult
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("References.Value() invalid JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0].KnowledgeID != "k1" || decoded[0].KnowledgeBaseID != "kb1" {
		t.Fatalf("References.Value() decoded = %#v", decoded)
	}
}

func TestReferencesScanAcceptsBytesAndString(t *testing.T) {
	for _, value := range []any{
		[]byte(`[{"id":"r1","knowledge_id":"k1","knowledge_base_id":"kb1","chunk_index":2}]`),
		`[{"id":"r1","knowledge_id":"k1","knowledge_base_id":"kb1","chunk_index":2}]`,
	} {
		var refs References
		if err := refs.Scan(value); err != nil {
			t.Fatalf("References.Scan(%T) error = %v", value, err)
		}
		if len(refs) != 1 || refs[0].KnowledgeID != "k1" || refs[0].KnowledgeBaseID != "kb1" {
			t.Fatalf("References.Scan(%T) = %#v", value, refs)
		}
	}
}
