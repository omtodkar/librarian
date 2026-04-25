package cmd

import (
	"reflect"
	"testing"

	"librarian/internal/store"
)

// TestParseGCKinds covers the flag parsing: single kind, comma-separated
// list, "all" sentinel, the reachable empty path (explicit --kinds=""),
// and the validation path that rejects typos.
func TestParseGCKinds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "single_symbol",
			raw:  "symbol",
			want: []string{"symbol"},
		},
		{
			name: "comma_separated",
			raw:  "symbol,config_key",
			want: []string{"symbol", "config_key"},
		},
		{
			name: "all_expands_to_every_kind",
			raw:  "all",
			want: store.NodeKinds(),
		},
		{
			name: "empty_string_same_as_all",
			raw:  "",
			want: store.NodeKinds(),
		},
		{
			name: "whitespace_trimmed",
			raw:  " symbol , document ",
			want: []string{"symbol", "document"},
		},
		{
			name:    "unknown_kind_rejected",
			raw:     "symbol,bogus",
			wantErr: true,
		},
		{
			name:    "only_unknown_rejected",
			raw:     "bogus",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGCKinds(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got kinds=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
