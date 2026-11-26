package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/artifact/worker"
)

// Names Setup finds before it creates.
const (
	SetupPolicyName = "aiman-artifacts-viewers"
	SetupAppName    = "aiman artifacts"
	SetupWorkerName = "aiman-artifacts"
	// setupCompatDate is the Worker's compatibility date.
	setupCompatDate = "2026-11-01"
	// setupSession is the Access session and policy duration.
	setupSession = "24h"
)

// SetupConfig is what Setup builds on Cloudflare.
type SetupConfig struct {
	AccountID        string
	Zone             string   // "aiman.fyi"
	Host             string   // "aiman.fyi"
	Bucket           string   // "aiman-artifacts"
	TeamDomain       string   // "<team>.cloudflareaccess.com"
	IdentityProvider string   // Access IdP id (Google)
	Owners           []string // Worker OWNERS, lower case
	Viewers          []string // Access policy emails (owners plus anyone shared with)
	ViewerDomains    []string // Access policy email domains
}

// SetupResult names the Access app Setup made or found.
type SetupResult struct {
	AppID    string
	AUD      string
	PolicyID string
}

func (c SetupConfig) check() error {
	var missing []string
	for name, v := range map[string]string{
		"account": c.AccountID, "zone": c.Zone, "host": c.Host, "bucket": c.Bucket,
		"team domain": c.TeamDomain, "identity provider": c.IdentityProvider,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(lowerAll(c.Owners)) == 0 {
		missing = append(missing, "owners")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: setup needs %s", ErrInvalid, strings.Join(missing, ", "))
	}
	return nil
}

// Setup is idempotent: it finds by name before it creates. Order: zone id,
// bucket, r2.dev off, Access policy, Access app, Worker, workers.dev and
// previews off, route, DNS record. log gets one line per step.
func Setup(ctx context.Context, api *CloudflareAPI, cfg SetupConfig, log io.Writer) (SetupResult, error) {
	if log == nil {
		log = io.Discard
	}
	if err := cfg.check(); err != nil {
		return SetupResult{}, err
	}
	acct := "/accounts/" + url.PathEscape(strings.TrimSpace(cfg.AccountID))
	host := strings.ToLower(strings.TrimSpace(cfg.Host))
	bucket := strings.TrimSpace(cfg.Bucket)

	zoneID, err := setupZone(ctx, api, strings.TrimSpace(cfg.Zone))
	if err != nil {
		return SetupResult{}, err
	}
	fmt.Fprintf(log, "zone %s: %s\n", cfg.Zone, zoneID)

	if err := setupBucket(ctx, api, acct, bucket, log); err != nil {
		return SetupResult{}, err
	}

	var res SetupResult
	if res.PolicyID, err = setupPolicy(ctx, api, acct, cfg, log); err != nil {
		return SetupResult{}, err
	}
	if res.AppID, res.AUD, err = setupApp(ctx, api, acct, host, cfg.IdentityProvider, res.PolicyID, log); err != nil {
		return SetupResult{}, err
	}
	if err := setupWorker(ctx, api, acct, cfg, host, bucket, res.AUD, log); err != nil {
		return SetupResult{}, err
	}
	if err := setupRoute(ctx, api, zoneID, host+"/a*", log); err != nil {
		return SetupResult{}, err
	}
	if err := setupDNS(ctx, api, zoneID, host, log); err != nil {
		return SetupResult{}, err
	}
	return res, nil
}

func setupZone(ctx context.Context, api *CloudflareAPI, zone string) (string, error) {
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := api.call(ctx, "GET", "/zones?name="+url.QueryEscape(zone), nil, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, zone) && z.ID != "" {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: zone %s is not in this account", zone)
}

func setupBucket(ctx context.Context, api *CloudflareAPI, acct, bucket string, log io.Writer) error {
	path := acct + "/r2/buckets/" + url.PathEscape(bucket)
	err := api.call(ctx, "GET", path, nil, nil)
	switch {
	case IsCloudflareNotFound(err):
		if err := api.call(ctx, "POST", acct+"/r2/buckets", map[string]any{"name": bucket}, nil); err != nil {
			return err
		}
		fmt.Fprintf(log, "bucket %s: created\n", bucket)
	case err != nil:
		return err
	default:
		fmt.Fprintf(log, "bucket %s: exists\n", bucket)
	}
	var managed struct {
		Enabled bool `json:"enabled"`
	}
	if err := api.call(ctx, "GET", path+"/domains/managed", nil, &managed); err != nil {
		return err
	}
	if managed.Enabled {
		if err := api.call(ctx, "PUT", path+"/domains/managed", map[string]any{"enabled": false}, nil); err != nil {
			return err
		}
		fmt.Fprintln(log, "r2.dev: turned off")
	} else {
		fmt.Fprintln(log, "r2.dev: off")
	}
	return nil
}

type cfNamed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	AUD  string `json:"aud"`
}

func findNamed(ctx context.Context, api *CloudflareAPI, path, name string) (cfNamed, bool, error) {
	var list []cfNamed
	if err := api.call(ctx, "GET", path, nil, &list); err != nil {
		return cfNamed{}, false, err
	}
	for _, it := range list {
		if it.Name == name && it.ID != "" {
			return it, true, nil
		}
	}
	return cfNamed{}, false, nil
}

func setupPolicy(ctx context.Context, api *CloudflareAPI, acct string, cfg SetupConfig, log io.Writer) (string, error) {
	var include []map[string]any
	for _, e := range lowerAll(append(append([]string{}, cfg.Owners...), cfg.Viewers...)) {
		include = append(include, map[string]any{"email": map[string]string{"email": e}})
	}
	for _, d := range lowerAll(cfg.ViewerDomains) {
		include = append(include, map[string]any{"email_domain": map[string]string{"domain": d}})
	}
	body := map[string]any{
		"name": SetupPolicyName, "decision": "allow", "include": include, "session_duration": setupSession,
	}
	path := acct + "/access/policies"
	found, ok, err := findNamed(ctx, api, path, SetupPolicyName)
	if err != nil {
		return "", err
	}
	if ok {
		if err := api.call(ctx, "PUT", path+"/"+url.PathEscape(found.ID), body, nil); err != nil {
			return "", err
		}
		fmt.Fprintf(log, "access policy %s: updated (%d rules)\n", found.ID, len(include))
		return found.ID, nil
	}
	var made cfNamed
	if err := api.call(ctx, "POST", path, body, &made); err != nil {
		return "", err
	}
	if made.ID == "" {
		return "", errors.New("cloudflare: access policy created without an id")
	}
	fmt.Fprintf(log, "access policy %s: created (%d rules)\n", made.ID, len(include))
	return made.ID, nil
}

func setupApp(ctx context.Context, api *CloudflareAPI, acct, host, idp, policyID string, log io.Writer) (string, string, error) {
	body := map[string]any{
		"name":   SetupAppName,
		"type":   "self_hosted",
		"domain": host + "/a",
		"destinations": []map[string]string{
			{"type": "public", "uri": host + "/a"},
			{"type": "public", "uri": host + "/a/*"},
		},
		"allowed_idps":               []string{strings.TrimSpace(idp)},
		"auto_redirect_to_identity":  true,
		"same_site_cookie_attribute": "none",
		"http_only_cookie_attribute": true,
		"session_duration":           setupSession,
		"policies":                   []map[string]any{{"id": policyID, "precedence": 1}},
	}
	path := acct + "/access/apps"
	found, ok, err := findNamed(ctx, api, path, SetupAppName)
	if err != nil {
		return "", "", err
	}
	var app cfNamed
	verb := "created"
	if ok {
		err = api.call(ctx, "PUT", path+"/"+url.PathEscape(found.ID), body, &app)
		verb = "updated"
	} else {
		err = api.call(ctx, "POST", path, body, &app)
	}
	if err != nil {
		return "", "", err
	}
	if app.ID == "" {
		app.ID = found.ID
	}
	if app.AUD == "" {
		app.AUD = found.AUD
	}
	if app.ID == "" || app.AUD == "" {
		return "", "", errors.New("cloudflare: access app answered without id or aud")
	}
	fmt.Fprintf(log, "access app %s: %s\n", app.ID, verb)
	return app.ID, app.AUD, nil
}

func setupWorker(ctx context.Context, api *CloudflareAPI, acct string, cfg SetupConfig, host, bucket, aud string, log io.Writer) error {
	text := func(name, v string) map[string]string {
		return map[string]string{"type": "plain_text", "name": name, "text": v}
	}
	meta := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": setupCompatDate,
		"bindings": []map[string]string{
			{"type": "r2_bucket", "name": "BUCKET", "bucket_name": bucket},
			text("ARTIFACT_HOST", host),
			text("OWNERS", strings.Join(lowerAll(cfg.Owners), ",")),
			text("TEAM_DOMAIN", strings.ToLower(strings.TrimSpace(cfg.TeamDomain))),
			text("POLICY_AUD", aud),
		},
	}
	body, err := workerUpload(meta, worker.Script)
	if err != nil {
		return err
	}
	script := acct + "/workers/scripts/" + SetupWorkerName
	if err := api.call(ctx, "PUT", script, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(log, "worker %s: uploaded\n", SetupWorkerName)
	if err := api.call(ctx, "POST", script+"/subdomain", map[string]any{"enabled": false, "previews_enabled": false}, nil); err != nil {
		return err
	}
	fmt.Fprintln(log, "workers.dev and previews: off")
	return nil
}

// workerUpload is the multipart body of a module Worker upload: a metadata
// field and the module as a file part.
func workerUpload(meta map[string]any, src []byte) (*multipartBody, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(raw); err != nil {
		return nil, err
	}
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="worker.js"; filename="worker.js"`)
	h.Set("Content-Type", "application/javascript+module")
	if part, err = mw.CreatePart(h); err != nil {
		return nil, err
	}
	if _, err := part.Write(src); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return &multipartBody{data: buf.Bytes(), contentType: mw.FormDataContentType()}, nil
}

func setupRoute(ctx context.Context, api *CloudflareAPI, zoneID, pattern string, log io.Writer) error {
	path := "/zones/" + url.PathEscape(zoneID) + "/workers/routes"
	var routes []struct {
		ID      string `json:"id"`
		Pattern string `json:"pattern"`
		Script  string `json:"script"`
	}
	if err := api.call(ctx, "GET", path, nil, &routes); err != nil {
		return err
	}
	body := map[string]string{"pattern": pattern, "script": SetupWorkerName}
	for _, r := range routes {
		if r.Pattern != pattern {
			continue
		}
		if r.Script == SetupWorkerName {
			fmt.Fprintf(log, "route %s: exists\n", pattern)
			return nil
		}
		if err := api.call(ctx, "PUT", path+"/"+url.PathEscape(r.ID), body, nil); err != nil {
			return err
		}
		fmt.Fprintf(log, "route %s: moved from %q\n", pattern, r.Script)
		return nil
	}
	if err := api.call(ctx, "POST", path, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(log, "route %s: created\n", pattern)
	return nil
}

func setupDNS(ctx context.Context, api *CloudflareAPI, zoneID, host string, log io.Writer) error {
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	var recs []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := api.call(ctx, "GET", path+"?name="+url.QueryEscape(host), nil, &recs); err != nil {
		return err
	}
	for _, r := range recs {
		if strings.EqualFold(strings.TrimSuffix(r.Name, "."), host) {
			fmt.Fprintf(log, "dns %s: has a %s record\n", host, r.Type)
			return nil
		}
	}
	body := map[string]any{"type": "AAAA", "name": host, "content": "100::", "proxied": true, "comment": "aiman artifacts Worker route"}
	if err := api.call(ctx, "POST", path, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(log, "dns %s: proxied AAAA 100:: created\n", host)
	return nil
}

// lowerAll trims, lower-cases and de-duplicates, keeping the first order.
func lowerAll(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
