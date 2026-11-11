package invites

import (
	"strings"
	"testing"
)

func TestParseEmailCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain rows",
			in:   "alice@acme.com\nbob@acme.com\n",
			want: []string{"alice@acme.com", "bob@acme.com"},
		},
		{
			name: "header skipped, trimmed, lowercased, deduped",
			in:   "Email\n  Alice@Acme.com \nbob@acme.com\nbob@acme.com\n",
			want: []string{"alice@acme.com", "bob@acme.com"},
		},
		{
			name: "blank and malformed rows dropped",
			in:   "alice@acme.com\n\nnot-an-email\n,\ncarol@acme.com\n",
			want: []string{"alice@acme.com", "carol@acme.com"},
		},
		{
			name: "first column only of a multi-column row",
			in:   "dave@acme.com,Dave,Engineering\n",
			want: []string{"dave@acme.com"},
		},
		{
			name: "no header when first row is a real email",
			in:   "first@acme.com\nsecond@acme.com\n",
			want: []string{"first@acme.com", "second@acme.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEmailCSV(strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("parseEmailCSV: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
