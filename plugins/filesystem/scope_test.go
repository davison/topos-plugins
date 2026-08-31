package main

import "testing"

// --- scope.includes (D-03): exclude, then include-if-declared, then the default allowlist ---
// Written before scope.go.

func TestScope_NoExtrasIncludesOnlyTheDefaultAllowlist(t *testing.T) {
	s := newScope(nil)

	c, included, err := s.includes("invoice.pdf")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if !included {
		t.Fatal("expected invoice.pdf to be included by the default allowlist")
	}
	if c.kind != previewKindBytes {
		t.Fatalf("expected previewKindBytes, got %v", c.kind)
	}

	_, included, err = s.includes("archive.zip")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if included {
		t.Fatal("expected archive.zip (unknown extension, no extras) to be excluded")
	}
}

func TestScope_IncludeGlobWidensPastTheDefaultAllowlist(t *testing.T) {
	s := newScope(map[string]string{"include_glob": "**/*.zip"})

	_, included, err := s.includes("nested/archive.zip")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if !included {
		t.Fatal("expected archive.zip to be included via include_glob despite its unknown extension")
	}
}

func TestScope_IncludeGlobNarrowsPastTheDefaultAllowlist(t *testing.T) {
	s := newScope(map[string]string{"include_glob": "**/*.zip"})

	_, included, err := s.includes("invoice.pdf")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if included {
		t.Fatal("expected invoice.pdf to be excluded — a declared include_glob replaces the extension test entirely")
	}
}

func TestScope_ExcludeGlobWinsOverIncludeGlob(t *testing.T) {
	s := newScope(map[string]string{
		"include_glob": "**/*.pdf",
		"exclude_glob": "**/*.pdf",
	})

	_, included, err := s.includes("invoice.pdf")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if included {
		t.Fatal("expected exclude_glob to win over include_glob for the same file")
	}
}

func TestScope_PatternsAreAnchoredToTheSourceRootRelativePath(t *testing.T) {
	s := newScope(map[string]string{"include_glob": "receipts/**/*.pdf"})

	_, insideReceipts, err := s.includes("receipts/2026/invoice.pdf")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if !insideReceipts {
		t.Fatal("expected an arbitrary-depth path under receipts/ to match")
	}

	_, outsideReceipts, err := s.includes("other/2026/invoice.pdf")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if outsideReceipts {
		t.Fatal("expected a path anchored outside receipts/ not to match")
	}
}

func TestScope_CommaSeparatedPatternListsToleratesWhitespaceAndEmptySegments(t *testing.T) {
	s := newScope(map[string]string{"include_glob": " **/*.pdf , , **/*.md "})

	for _, path := range []string{"a.pdf", "a.md"} {
		_, included, err := s.includes(path)
		if err != nil {
			t.Fatalf("includes(%q): %v", path, err)
		}
		if !included {
			t.Fatalf("expected %q to be included", path)
		}
	}
}

func TestScope_UnknownExtensionIncludedByGlobIsMetadataOnly(t *testing.T) {
	s := newScope(map[string]string{"include_glob": "**/*.zip"})

	c, included, err := s.includes("archive.zip")
	if err != nil {
		t.Fatalf("includes: %v", err)
	}
	if !included {
		t.Fatal("expected archive.zip to be included")
	}
	if c.kind != previewKindMetadataOnly {
		t.Fatalf("expected previewKindMetadataOnly for an unknown extension included by glob, got %v", c.kind)
	}
	if c.mime != "" {
		t.Fatalf("expected no guessed mime type, got %q", c.mime)
	}
}

func TestScope_MalformedGlobPatternIsReportedAsANamedError(t *testing.T) {
	s := newScope(map[string]string{"include_glob": "[unterminated"})

	_, _, err := s.includes("invoice.pdf")
	if err == nil {
		t.Fatal("expected a malformed glob pattern to be reported as an error, got nil")
	}
}
