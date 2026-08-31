package main

import "testing"

// --- classify (D-03/D-04): extension -> {preview kind, mime}, written before classify.go ---

func TestClassify_PDFIsBytesKindWithPDFMime(t *testing.T) {
	c, ok := classify("invoice.pdf")
	if !ok {
		t.Fatal("expected .pdf to be in the default allowlist")
	}
	if c.kind != previewKindBytes {
		t.Fatalf("expected previewKindBytes, got %v", c.kind)
	}
	if c.mime != "application/pdf" {
		t.Fatalf("expected mime application/pdf, got %q", c.mime)
	}
}

func TestClassify_ImagesAreBytesKindWithOwnMime(t *testing.T) {
	cases := map[string]string{
		"photo.png":  "image/png",
		"photo.jpg":  "image/jpeg",
		"photo.jpeg": "image/jpeg",
		"photo.gif":  "image/gif",
		"photo.webp": "image/webp",
	}
	for name, wantMime := range cases {
		c, ok := classify(name)
		if !ok {
			t.Fatalf("%s: expected to be in the default allowlist", name)
		}
		if c.kind != previewKindBytes {
			t.Fatalf("%s: expected previewKindBytes, got %v", name, c.kind)
		}
		if c.mime != wantMime {
			t.Fatalf("%s: expected mime %q, got %q", name, wantMime, c.mime)
		}
	}
}

func TestClassify_MarkdownExtensionsAreMarkdownKind(t *testing.T) {
	for _, name := range []string{"notes.md", "notes.markdown"} {
		c, ok := classify(name)
		if !ok {
			t.Fatalf("%s: expected to be in the default allowlist", name)
		}
		if c.kind != previewKindMarkdown {
			t.Fatalf("%s: expected previewKindMarkdown, got %v", name, c.kind)
		}
	}
}

func TestClassify_PlainTextExtensionsAreTextPlainKind(t *testing.T) {
	for _, name := range []string{"a.txt", "a.text", "a.log", "a.csv"} {
		c, ok := classify(name)
		if !ok {
			t.Fatalf("%s: expected to be in the default allowlist", name)
		}
		if c.kind != previewKindPlainText {
			t.Fatalf("%s: expected previewKindPlainText, got %v", name, c.kind)
		}
	}
}

func TestClassify_OfficeFormatsAreMetadataOnlyWithNoMime(t *testing.T) {
	for _, name := range []string{
		"a.doc", "a.docx", "a.xls", "a.xlsx", "a.ppt", "a.pptx",
		"a.odt", "a.ods", "a.odp", "a.rtf",
	} {
		c, ok := classify(name)
		if !ok {
			t.Fatalf("%s: expected to be in the default allowlist", name)
		}
		if c.kind != previewKindMetadataOnly {
			t.Fatalf("%s: expected previewKindMetadataOnly, got %v", name, c.kind)
		}
		if c.mime != "" {
			t.Fatalf("%s: expected no mime type, got %q", name, c.mime)
		}
	}
}

func TestClassify_UnrenderableImagesAreMetadataOnlyButInAllowlist(t *testing.T) {
	for _, name := range []string{"a.svg", "a.bmp", "a.tif", "a.tiff", "a.heic"} {
		c, ok := classify(name)
		if !ok {
			t.Fatalf("%s: expected to be present in the default allowlist", name)
		}
		if c.kind != previewKindMetadataOnly {
			t.Fatalf("%s: expected previewKindMetadataOnly, got %v", name, c.kind)
		}
	}
}

func TestClassify_ExtensionMatchingIsCaseInsensitive(t *testing.T) {
	lower, okLower := classify("report.pdf")
	upper, okUpper := classify("REPORT.PDF")
	if !okLower || !okUpper {
		t.Fatal("expected both cases to be recognized")
	}
	if lower != upper {
		t.Fatalf("expected identical classification regardless of case, got %+v vs %+v", lower, upper)
	}
}

func TestClassify_ExtensionOutsideTableIsNotInDefaultAllowlist(t *testing.T) {
	_, ok := classify("archive.zip")
	if ok {
		t.Fatal("expected .zip to be reported as outside the default allowlist")
	}
}
