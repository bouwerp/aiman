package jira

import (
	"strings"
	"testing"
)

func TestFormatIssueDescription_PlainString(t *testing.T) {
	got := formatIssueDescription("  hello world  ")
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatIssueDescription_ADFParagraph(t *testing.T) {
	desc := map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Second sync returns "},
					map[string]interface{}{
						"type":  "text",
						"text":  "HTTP 500",
						"marks": []interface{}{map[string]interface{}{"type": "strong"}},
					},
					map[string]interface{}{"type": "text", "text": " when called twice."},
				},
			},
		},
	}
	got := formatIssueDescription(desc)
	if !strings.Contains(got, "Second sync returns") || !strings.Contains(got, "**HTTP 500**") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestFormatIssueDescription_ADFListAndLink(t *testing.T) {
	desc := map[string]interface{}{
		"type": "doc",
		"content": []interface{}{
			map[string]interface{}{
				"type": "bulletList",
				"content": []interface{}{
					map[string]interface{}{
						"type": "listItem",
						"content": []interface{}{
							map[string]interface{}{
								"type": "paragraph",
								"content": []interface{}{
									map[string]interface{}{"type": "text", "text": "Repro: call twice"},
								},
							},
						},
					},
					map[string]interface{}{
						"type": "listItem",
						"content": []interface{}{
							map[string]interface{}{
								"type": "paragraph",
								"content": []interface{}{
									map[string]interface{}{
										"type": "text",
										"text": "Docs",
										"marks": []interface{}{
											map[string]interface{}{
												"type": "link",
												"attrs": map[string]interface{}{
													"href": "https://example.com/doc",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	got := formatIssueDescription(desc)
	if !strings.Contains(got, "- Repro: call twice") {
		t.Fatalf("expected bullet, got:\n%s", got)
	}
	if !strings.Contains(got, "[Docs](https://example.com/doc)") {
		t.Fatalf("expected link, got:\n%s", got)
	}
}

func TestFormatIssueDescription_Nil(t *testing.T) {
	if formatIssueDescription(nil) != "" {
		t.Fatal("expected empty")
	}
}
