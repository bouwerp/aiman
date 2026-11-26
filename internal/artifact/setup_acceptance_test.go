package artifact

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Acceptance tests for docs/specs/artifacts.md piece C (setup). Do not edit.

// fakeCF is a minimal Cloudflare API: it records calls and keeps state so a
// second Setup can be checked for idempotence.
type fakeCF struct {
	mu       sync.Mutex
	calls    []string
	buckets  map[string]bool
	managed  bool // r2.dev public access
	script   map[string]any
	subdom   map[string]any
	routes   []string
	dns      []string
	policies map[string]string // name -> id
	apps     map[string]map[string]any
	authOK   bool
}

func newFakeCF() *fakeCF {
	return &fakeCF{buckets: map[string]bool{}, policies: map[string]string{}, apps: map[string]map[string]any{}, authOK: true, managed: true}
}

func (f *fakeCF) ok(w http.ResponseWriter, result any) {
	json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/client/v4")
	f.calls = append(f.calls, r.Method+" "+p)
	acct := "/accounts/acc1"
	switch {
	case r.Method == "GET" && p == "/zones" && r.URL.Query().Get("name") == "aiman.fyi":
		f.ok(w, []any{map[string]any{"id": "zone1", "name": "aiman.fyi"}})
	case r.Method == "GET" && p == acct+"/r2/buckets/aiman-artifacts":
		if !f.buckets["aiman-artifacts"] {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10006, "message": "bucket not found"}}})
			return
		}
		f.ok(w, map[string]any{"name": "aiman-artifacts"})
	case r.Method == "POST" && p == acct+"/r2/buckets":
		f.buckets["aiman-artifacts"] = true
		f.ok(w, map[string]any{"name": "aiman-artifacts"})
	case r.Method == "GET" && p == acct+"/r2/buckets/aiman-artifacts/domains/managed":
		f.ok(w, map[string]any{"enabled": f.managed})
	case r.Method == "PUT" && p == acct+"/r2/buckets/aiman-artifacts/domains/managed":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.managed = b["enabled"] == true
		f.ok(w, map[string]any{"enabled": f.managed})
	case r.Method == "GET" && p == acct+"/access/policies":
		var out []any
		for n, id := range f.policies {
			out = append(out, map[string]any{"id": id, "name": n})
		}
		f.ok(w, out)
	case r.Method == "POST" && p == acct+"/access/policies":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		id := "pol" + string(rune('0'+len(f.policies)))
		f.policies[b["name"].(string)] = id
		f.ok(w, map[string]any{"id": id, "name": b["name"]})
	case r.Method == "PUT" && strings.HasPrefix(p, acct+"/access/policies/"):
		f.ok(w, map[string]any{"id": strings.TrimPrefix(p, acct+"/access/policies/")})
	case r.Method == "GET" && p == acct+"/access/apps":
		var out []any
		for _, a := range f.apps {
			out = append(out, a)
		}
		f.ok(w, out)
	case r.Method == "POST" && p == acct+"/access/apps":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		b["id"] = "app1"
		b["aud"] = "aud-123"
		f.apps["app1"] = b
		f.ok(w, b)
	case r.Method == "PUT" && p == acct+"/access/apps/app1":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		b["id"] = "app1"
		b["aud"] = "aud-123"
		f.apps["app1"] = b
		f.ok(w, b)
	case r.Method == "PUT" && p == acct+"/workers/scripts/aiman-artifacts":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		var meta map[string]any
		json.Unmarshal([]byte(r.FormValue("metadata")), &meta)
		file, _, err := r.FormFile("worker.js")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		src, _ := io.ReadAll(file)
		meta["_src"] = string(src)
		f.script = meta
		f.ok(w, map[string]any{"id": "aiman-artifacts"})
	case r.Method == "POST" && p == acct+"/workers/scripts/aiman-artifacts/subdomain":
		json.NewDecoder(r.Body).Decode(&f.subdom)
		f.ok(w, f.subdom)
	case r.Method == "GET" && p == "/zones/zone1/workers/routes":
		var out []any
		for _, rt := range f.routes {
			out = append(out, map[string]any{"id": "r-" + rt, "pattern": rt, "script": "aiman-artifacts"})
		}
		f.ok(w, out)
	case r.Method == "POST" && p == "/zones/zone1/workers/routes":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.routes = append(f.routes, b["pattern"].(string))
		f.ok(w, b)
	case r.Method == "GET" && p == "/zones/zone1/dns_records":
		var out []any
		for _, d := range f.dns {
			out = append(out, map[string]any{"type": "AAAA", "name": d, "content": "100::", "proxied": true})
		}
		f.ok(w, out)
	case r.Method == "POST" && p == "/zones/zone1/dns_records":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["proxied"] != true {
			w.WriteHeader(400)
			return
		}
		f.dns = append(f.dns, b["name"].(string))
		f.ok(w, b)
	default:
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 7003, "message": "no route " + r.Method + " " + p}}})
	}
}

func setupCfg() SetupConfig {
	return SetupConfig{
		AccountID: "acc1", Zone: "aiman.fyi", Host: "aiman.fyi", Bucket: "aiman-artifacts",
		TeamDomain: "team.cloudflareaccess.com", IdentityProvider: "idp-google",
		Owners: []string{"Me@Example.com"}, Viewers: []string{"me@example.com"},
	}
}

func bindings(f *fakeCF) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, b := range f.script["bindings"].([]any) {
		m := b.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func TestSetupCreatesEverythingOnce(t *testing.T) {
	f := newFakeCF()
	srv := httptest.NewServer(f)
	defer srv.Close()
	api := &CloudflareAPI{Base: srv.URL + "/client/v4", Token: "tok", HTTP: srv.Client()}
	ctx := context.Background()

	res, err := Setup(ctx, api, setupCfg(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.AUD != "aud-123" || res.AppID != "app1" {
		t.Fatalf("result %+v", res)
	}
	if !f.buckets["aiman-artifacts"] || f.managed {
		t.Fatalf("bucket %v, r2.dev public %v", f.buckets, f.managed)
	}
	app := f.apps["app1"]
	if app["type"] != "self_hosted" || app["same_site_cookie_attribute"] != "none" || app["auto_redirect_to_identity"] != true {
		t.Fatalf("app %+v", app)
	}
	dests, _ := json.Marshal(app["destinations"])
	if !strings.Contains(string(dests), `"aiman.fyi/a"`) || !strings.Contains(string(dests), `"aiman.fyi/a/*"`) {
		t.Fatalf("destinations %s", dests)
	}
	idps, _ := json.Marshal(app["allowed_idps"])
	if string(idps) != `["idp-google"]` {
		t.Fatalf("allowed_idps %s", idps)
	}
	if f.script == nil || !strings.Contains(f.script["_src"].(string), "Cf-Access-Jwt-Assertion") {
		t.Fatal("worker.js not uploaded")
	}
	if f.script["main_module"] != "worker.js" {
		t.Fatalf("main_module %v", f.script["main_module"])
	}
	bs := bindings(f)
	if bs["BUCKET"]["type"] != "r2_bucket" || bs["BUCKET"]["bucket_name"] != "aiman-artifacts" {
		t.Fatalf("bucket binding %+v", bs["BUCKET"])
	}
	for name, want := range map[string]string{"ARTIFACT_HOST": "aiman.fyi", "TEAM_DOMAIN": "team.cloudflareaccess.com", "POLICY_AUD": "aud-123", "OWNERS": "me@example.com"} {
		if bs[name]["text"] != want {
			t.Fatalf("binding %s = %v, want %s", name, bs[name]["text"], want)
		}
	}
	if f.subdom["enabled"] != false || f.subdom["previews_enabled"] != false {
		t.Fatalf("workers.dev %+v", f.subdom)
	}
	if strings.Join(f.routes, ",") != "aiman.fyi/a*" {
		t.Fatalf("routes %v", f.routes)
	}
	if strings.Join(f.dns, ",") != "aiman.fyi" {
		t.Fatalf("dns %v", f.dns)
	}
	// Access must exist before the Worker can verify its tokens.
	ai, wi := -1, -1
	for i, c := range f.calls {
		if c == "POST /accounts/acc1/access/apps" && ai < 0 {
			ai = i
		}
		if c == "PUT /accounts/acc1/workers/scripts/aiman-artifacts" && wi < 0 {
			wi = i
		}
	}
	if ai < 0 || wi < 0 || ai > wi {
		t.Fatalf("order: access app at %d, worker at %d: %v", ai, wi, f.calls)
	}

	// A second run changes nothing it does not have to.
	f.calls = nil
	if _, err := Setup(ctx, api, setupCfg(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.routes) != 1 || len(f.dns) != 1 || len(f.policies) != 1 || len(f.apps) != 1 {
		t.Fatalf("second run duplicated: routes %v dns %v policies %v apps %d", f.routes, f.dns, f.policies, len(f.apps))
	}
	for _, c := range f.calls {
		if c == "POST /accounts/acc1/r2/buckets" || c == "POST /accounts/acc1/access/apps" || c == "POST /accounts/acc1/access/policies" {
			t.Fatalf("second run created again: %s", c)
		}
	}
}

func TestSetupReportsAPIErrors(t *testing.T) {
	f := newFakeCF()
	srv := httptest.NewServer(f)
	defer srv.Close()
	api := &CloudflareAPI{Base: srv.URL + "/client/v4", Token: "wrong", HTTP: srv.Client()}
	_, err := Setup(context.Background(), api, setupCfg(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("error message carries the token")
	}
}
