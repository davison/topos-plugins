package searchkit

import (
	"strings"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRequireMembership(t *testing.T) {
	if err := RequireMembership(&toposv1.SearchRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("absent map: %v", err)
	}
	if err := RequireMembership(&toposv1.SearchRequest{MatchFields: map[string]*toposv1.StringList{"labels": {}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a key with no values is still no membership: %v", err)
	}
	if err := RequireMembership(&toposv1.SearchRequest{MatchFields: map[string]*toposv1.StringList{"labels": {Values: []string{"x"}}}}); err != nil {
		t.Fatalf("membership present: %v", err)
	}
}

func TestTermsMatchesAndMatchedIn(t *testing.T) {
	terms := Terms("  Boiler  Quote a ")
	if len(terms) != 2 || terms[0] != "boiler" || terms[1] != "quote" {
		t.Fatalf("terms: %v", terms)
	}
	req := &toposv1.SearchRequest{RequiredTerms: []string{" Invoice ", ""}}
	if r := Required(req); len(r) != 1 || r[0] != "invoice" {
		t.Fatalf("required: %v", r)
	}
	if !Matches("Boiler quote", "", "the invoice is attached", nil, terms, Required(req)) {
		t.Error("query in title and required in body must match")
	}
	if Matches("Boiler quote", "", "no receipt here", nil, terms, Required(req)) {
		t.Error("a missing required term must not match")
	}
	if MatchedIn("Boiler quote", "", nil, terms) != toposv1.MatchedIn_MATCHED_IN_TITLE {
		t.Error("title")
	}
	if MatchedIn("hello", "the boiler quote arrived", nil, terms) != toposv1.MatchedIn_MATCHED_IN_BODY {
		t.Error("body")
	}
	if MatchedIn("hello", "", []string{"boiler", "quote"}, terms) != toposv1.MatchedIn_MATCHED_IN_LABELS {
		t.Error("labels")
	}
}

func TestSnippetIsBoundedAndNeverTheBody(t *testing.T) {
	body := strings.Repeat("lorem ipsum ", 200) + "the BOILER is here " + strings.Repeat("dolor sit ", 200)
	s := Snippet(body, []string{"boiler"})
	if !strings.Contains(s, "BOILER") || len([]rune(s)) > 2*SnippetWindow+2 || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("snippet: %q (%d runes)", s, len([]rune(s)))
	}
	if Snippet("", []string{"x"}) != "" {
		t.Error("empty body, empty snippet")
	}
	if got := Snippet("héllo wörld", []string{"wörld"}); !strings.Contains(got, "wörld") {
		t.Errorf("rune-safe: %q", got)
	}
}

func TestLimit(t *testing.T) {
	hits := []*toposv1.SearchHit{{}, {}, {}}
	got, trunc := Limit(hits, &toposv1.SearchRequest{Limit: 2})
	if len(got) != 2 || !trunc {
		t.Errorf("limit 2: %d %v", len(got), trunc)
	}
	got, trunc = Limit(nil, &toposv1.SearchRequest{})
	if got == nil || len(got) != 0 || trunc {
		t.Errorf("nil in, empty slice out: %v %v", got, trunc)
	}
}

func TestRequireMembership_OwnFieldsOnly(t *testing.T) {
	foreign := &toposv1.SearchRequest{MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{"house"}}}}
	if err := RequireMembership(foreign, "folders"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign-only map: got %v, want InvalidArgument", err)
	}
	if err := RequireMembership(foreign); err != nil {
		t.Fatalf("unscoped guard should accept any populated field: %v", err)
	}
	own := &toposv1.SearchRequest{MatchFields: map[string]*toposv1.StringList{"tags": {Values: []string{}}, "pages": {Values: []string{"Home"}}}}
	if err := RequireMembership(own, "tags", "pages"); err != nil {
		t.Fatalf("one populated own field suffices: %v", err)
	}
}
