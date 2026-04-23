package report

import "testing"

func TestCommunityColor(t *testing.T) {
	cases := []struct {
		name  string
		id    int
		wantGrey bool
	}{
		{"community_none_sentinel", -1, true},
		{"negative_other", -42, true},
		{"first_palette_entry", 0, false},
		{"second_palette_entry", 1, false},
		{"wrap_around_matches_modulo", 10, false}, // 10 % 10 = 0 — same colour as id 0
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := communityColor(tc.id)
			if tc.wantGrey {
				if got != "#999999" {
					t.Errorf("communityColor(%d) = %q, want grey fallback %q", tc.id, got, "#999999")
				}
				return
			}
			if got == "" {
				t.Errorf("communityColor(%d) returned empty string", tc.id)
			}
			if got == "#999999" {
				t.Errorf("communityColor(%d) returned grey fallback for a valid id", tc.id)
			}
		})
	}

	// Wrap-around: id N and id N+palette_size resolve to the same colour.
	if communityColor(0) != communityColor(10) {
		t.Errorf("expected id 0 and id 10 to wrap to the same palette entry; got %q vs %q",
			communityColor(0), communityColor(10))
	}
}

func TestEscapeMarkdownTableCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no_change", "plain text", "plain text"},
		{"pipe_escaped", "a|b", `a\|b`},
		{"multiple_pipes", "a|b|c", `a\|b\|c`},
		{"lf_replaced_with_space", "line1\nline2", "line1 line2"},
		{"cr_replaced_with_space", "line1\rline2", "line1 line2"},
		{"crlf_replaced_with_space", "line1\r\nline2", "line1 line2"},
		{"pipe_and_newline", "a|b\nc", `a\|b c`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeMarkdownTableCell(tc.in)
			if got != tc.want {
				t.Errorf("escapeMarkdownTableCell(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
