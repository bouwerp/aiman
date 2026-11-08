package apicompat

import (
	"strings"
	"testing"
)

const unionSpec = `openapi: 3.1.0
info:
  version: 1.0.0
components:
  schemas:
    Response:
      type: object
      properties:
        result:
          oneOf:
            - $ref: "#/components/schemas/A"
            - $ref: "#/components/schemas/B"
            - type: object
              properties:
                n:
                  type: integer
    A:
      type: object
    B:
      type: object
    C:
      type: object
`

func TestCompareSpecsUnionBranchesAreASet(t *testing.T) {
	inserted := strings.Replace(unionSpec,
		"            - $ref: \"#/components/schemas/A\"\n",
		"            - $ref: \"#/components/schemas/C\"\n            - $ref: \"#/components/schemas/A\"\n", 1)
	if got := CompareSpecs([]byte(unionSpec), []byte(inserted)); len(got) != 0 {
		t.Fatalf("inserting a branch reported problems: %q", got)
	}

	dropped := strings.Replace(unionSpec, "            - $ref: \"#/components/schemas/B\"\n", "", 1)
	got := CompareSpecs([]byte(unionSpec), []byte(dropped))
	if len(got) != 1 || !strings.Contains(got[0], "schemas/B") {
		t.Fatalf("dropping branch B: got %q", got)
	}

	retyped := strings.Replace(unionSpec, "                  type: integer\n", "                  type: string\n", 1)
	got = CompareSpecs([]byte(unionSpec), []byte(retyped))
	if len(got) != 1 || !strings.Contains(got[0], "oneOf[0].n") {
		t.Fatalf("retyping an inline branch property: got %q", got)
	}
}

func TestJSONNameFollowsEncodingJSON(t *testing.T) {
	src := []byte("package p\n\ntype T struct {\n\tA string `json:\"-\"`\n\tB string `json:\"-,\"`\n\tC string `json:\",omitempty\"`\n\tD string\n}\n")
	fields, err := jsonFields(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"-": true, "C": true, "D": true}
	if len(fields["T"]) != len(want) {
		t.Fatalf("fields = %v, want names %v", fields["T"], want)
	}
	for name := range want {
		if _, ok := fields["T"][name]; !ok {
			t.Fatalf("missing json name %q in %v", name, fields["T"])
		}
	}
}
