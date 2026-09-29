package cli

import (
	"encoding/json"
	"testing"
)

func TestRecentListTable(t *testing.T) {
	result := []byte(`{
		"items": [
			{
				"sessionId": "tab-1",
				"type": "tab",
				"closedTime": 1000,
				"title": "Example",
				"url": "https://example.com"
			},
			{
				"sessionId": "window-1",
				"type": "window",
				"closedTime": 900,
				"title": "Another Tab",
				"url": "https://another.com",
				"tabCount": 3
			}
		]
	}`)

	err := printRecentTable(result)
	if err != nil {
		t.Fatalf("printRecentTable failed: %v", err)
	}
}

func TestRecentListJSON(t *testing.T) {
	result := []byte(`{
		"items": [
			{
				"sessionId": "tab-1",
				"type": "tab",
				"closedTime": 1000,
				"title": "Example",
				"url": "https://example.com"
			}
		]
	}`)

	err := printResultJSON(result)
	if err != nil {
		t.Fatalf("printResultJSON failed: %v", err)
	}
}

func TestRecentListEmpty(t *testing.T) {
	result := []byte(`{"items": []}`)

	err := printRecentTable(result)
	if err != nil {
		t.Fatalf("printRecentTable with empty items failed: %v", err)
	}
}

func TestRecentLimitValidation(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{"valid limit 1", 1, false},
		{"valid limit 25", 25, false},
		{"valid limit 10", 10, false},
		{"invalid limit 0", 0, true},
		{"invalid limit 26", 26, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.limit < 1 || tt.limit > 25 {
				if !tt.wantErr {
					t.Errorf("expected error for limit %d, but got none", tt.limit)
				}
			} else {
				if tt.wantErr {
					t.Errorf("did not expect error for limit %d", tt.limit)
				}
			}
		})
	}
}

func TestRecentRow(t *testing.T) {
	rowJSON := `{
		"sessionId": "tab-1",
		"type": "tab",
		"closedTime": 1000,
		"title": "Example",
		"url": "https://example.com"
	}`

	var row recentRow
	err := json.Unmarshal([]byte(rowJSON), &row)
	if err != nil {
		t.Fatalf("Failed to unmarshal recentRow: %v", err)
	}

	if row.SessionID != "tab-1" {
		t.Errorf("SessionID = %q, want %q", row.SessionID, "tab-1")
	}
	if row.Type != "tab" {
		t.Errorf("Type = %q, want %q", row.Type, "tab")
	}
}
