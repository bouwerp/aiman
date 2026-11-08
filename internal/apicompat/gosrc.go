package apicompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// deviceSets are the gateway allowlist variables a phone depends on.
// gatewayMethods is not one: the gateway answers those itself.
var deviceSets = []string{"deviceReadMethods", "deviceWriteMethods", "deviceDestructiveMethods"}

// CompareAllowlists reports every method the previous allowlist source held in
// a device set that the new source no longer holds in the same set, so a
// method an older phone calls is never refused by a newer gateway.
func CompareAllowlists(oldGo, newGo []byte) []string {
	oldSets, err := stringSlices(oldGo, deviceSets)
	if err != nil {
		return []string{"previous allowlist: " + err.Error()}
	}
	newSets, err := stringSlices(newGo, deviceSets)
	if err != nil {
		return []string{"allowlist: " + err.Error()}
	}
	var problems []string
	for _, name := range deviceSets {
		old, had := oldSets[name]
		if !had {
			continue
		}
		cur, has := newSets[name]
		if !has {
			problems = append(problems, fmt.Sprintf("allowlist: %s removed", name))
			continue
		}
		for _, m := range old {
			if !slices.Contains(cur, m) {
				problems = append(problems, fmt.Sprintf("allowlist: %s no longer holds %q", name, m))
			}
		}
	}
	return problems
}

// stringSlices returns the string elements of each package-level variable in
// names whose value is a []string composite literal.
func stringSlices(src []byte, names []string) (map[string][]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vs.Names {
				if i >= len(vs.Values) || !slices.Contains(names, ident.Name) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("%s is not a composite literal", ident.Name)
				}
				var vals []string
				for _, elt := range lit.Elts {
					bl, ok := elt.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						return nil, fmt.Errorf("%s holds a non-literal element", ident.Name)
					}
					v, err := strconv.Unquote(bl.Value)
					if err != nil {
						return nil, fmt.Errorf("%s: %w", ident.Name, err)
					}
					vals = append(vals, v)
				}
				out[ident.Name] = vals
			}
		}
	}
	return out, nil
}

// CompareJSONNames reports every JSON name of the named structs in oldGo that
// newGo drops or gives a different Go type. Go field names may change; the
// JSON name is the contract.
func CompareJSONNames(oldGo, newGo []byte, typeNames []string) []string {
	oldStructs, err := jsonFields(oldGo)
	if err != nil {
		return []string{"previous source: " + err.Error()}
	}
	newStructs, err := jsonFields(newGo)
	if err != nil {
		return []string{"source: " + err.Error()}
	}
	var problems []string
	for _, name := range typeNames {
		old, had := oldStructs[name]
		if !had {
			continue
		}
		cur, has := newStructs[name]
		if !has {
			problems = append(problems, fmt.Sprintf("struct %s removed", name))
			continue
		}
		for _, jn := range sortedKeys(old) {
			typ, ok := cur[jn]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s: json field %q removed", name, jn))
			case typ != old[jn]:
				problems = append(problems, fmt.Sprintf("%s: json field %q changed type %s -> %s", name, jn, old[jn], typ))
			}
		}
	}
	return problems
}

// jsonFields maps each struct type in src to its JSON names and the source
// text of their Go types, following encoding/json's naming: a tag name, else
// the exported field name; "-" and unexported fields are not encoded.
func jsonFields(src []byte) (map[string]map[string]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		fields := map[string]string{}
		for _, f := range st.Fields.List {
			typ := types.ExprString(f.Type)
			for _, name := range f.Names {
				if jn, ok := jsonName(name.Name, f.Tag); ok {
					fields[jn] = typ
				}
			}
		}
		out[ts.Name.Name] = fields
		return false
	})
	return out, nil
}

func jsonName(field string, tag *ast.BasicLit) (string, bool) {
	if !ast.IsExported(field) {
		return "", false
	}
	if tag == nil {
		return field, true
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return field, true
	}
	val := reflect.StructTag(raw).Get("json")
	if val == "-" {
		return "", false
	}
	if name, _, _ := strings.Cut(val, ","); name != "" {
		return name, true
	}
	return field, true
}
