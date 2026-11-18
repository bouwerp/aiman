package awsiac

import "fmt"

// applyGlobals merges SAM's Globals.Function into every
// AWS::Serverless::Function and Globals.SimpleTable into every
// AWS::Serverless::SimpleTable, as the SAM transform does: mappings merge
// key by key with the resource winning, lists are concatenated (globals
// first), and any other value of the resource replaces the global one.
func (s *cfnStack) applyGlobals() {
	g := s.tmpl.get("Globals")
	if g == nil || g.kind != cfnMap {
		return
	}
	for _, id := range s.order {
		r := s.res[id]
		var global *cfnNode
		switch r.typ {
		case typeSAMFunction:
			global = g.get("Function")
		case "AWS::Serverless::SimpleTable":
			global = g.get("SimpleTable")
		}
		if global != nil {
			r.props = samMerge(global, r.props)
		}
	}
}

// samMerge merges a global node under a resource's node into a new node;
// neither input is changed.
func samMerge(global, local *cfnNode) *cfnNode {
	switch {
	case local == nil:
		return global
	case global == nil:
		return local
	case global.kind == cfnMap && local.kind == cfnMap && !intrinsicNode(global) && !intrinsicNode(local):
		out := &cfnNode{kind: cfnMap, line: local.line}
		for i, k := range global.keys {
			out.set(k, global.vals[i])
		}
		for i, k := range local.keys {
			out.set(k, samMerge(out.get(k), local.vals[i]))
		}
		return out
	case global.kind == cfnSeq && local.kind == cfnSeq:
		out := &cfnNode{kind: cfnSeq, line: local.line}
		out.items = append(append(out.items, global.items...), local.items...)
		return out
	}
	return local
}

func intrinsicNode(n *cfnNode) bool {
	return n.kind == cfnMap && len(n.keys) == 1 && (isIntrinsic(n.keys[0]) || n.keys[0] == "Condition")
}

// simpleTable plans an AWS::Serverless::SimpleTable: PrimaryKey (default
// id, String), ProvisionedThroughput (else on demand), Tags.
func (s *cfnStack) simpleTable(b *build) error {
	name, ok := b.idString("TableName")
	if !ok {
		return nil
	}
	key, typ := "id", "S"
	if pk := b.prop("PrimaryKey"); pk != nil {
		if !b.identity("PrimaryKey", pk) {
			return nil
		}
		n, _ := pk.field("Name").literal()
		t, _ := pk.field("Type").literal()
		if n == "" {
			b.skip("PrimaryKey.Name", pk, "PrimaryKey has no Name")
			return nil
		}
		key = n
		switch t {
		case "String":
			typ = "S"
		case "Number":
			typ = "N"
		case "Binary":
			typ = "B"
		default:
			b.skip("PrimaryKey.Type", pk, fmt.Sprintf("PrimaryKey type %q is not String, Number or Binary", t))
			return nil
		}
	}
	t := &Table{HashKey: key, Attributes: []AttributeDef{{Name: key, Type: typ}}, BillingMode: "PAY_PER_REQUEST"}
	if pt := b.prop("ProvisionedThroughput"); b.ok("ProvisionedThroughput", pt) {
		t.BillingMode = "PROVISIONED"
		if rc, ok := b.intOf("ProvisionedThroughput.ReadCapacityUnits", pt.field("ReadCapacityUnits")); ok {
			t.ReadCapacity = rc
		}
		if wc, ok := b.intOf("ProvisionedThroughput.WriteCapacityUnits", pt.field("WriteCapacityUnits")); ok {
			t.WriteCapacity = wc
		}
	}
	if sse := b.prop("SSESpecification"); sse != nil {
		b.note("SSESpecification", sse, "encryption settings are not emulated; left out")
	}
	t.Tags = b.tags()
	return b.finish(&Resource{Kind: KindTable, Name: name, Table: t})
}

// samEvents notes a SAM function's event sources: the function never
// runs, so nothing invokes it.
func (s *cfnStack) samEvents(r *cfnResource) {
	ev := r.props.get("Events")
	if ev == nil || ev.kind != cfnMap {
		return
	}
	for i, name := range ev.keys {
		e := ev.vals[i]
		typ, _ := scalarText(e.get("Type"))
		s.p.Note(Warning{Stack: s.src.Name, Resource: r.id, Attribute: "Events." + name, File: s.src.Path, Line: e.line,
			Message: fmt.Sprintf("not emulated: event source %s for %s", typ, r.id)})
	}
}
