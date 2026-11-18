package edge

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAWSProxyBlocksRealAWS refuses AWS hosts before stubs, online and
// offline, and leaves other hosts alone.
func TestAWSProxyBlocksRealAWS(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("upstream " + r.Host))
	}))
	defer up.Close()
	blocks := []string{"amazonaws.com", ".amazonaws.com.cn", "API.aws", "on.aws"}
	for _, online := range []bool{false, true} {
		cfg := ProxyConfig{
			Online: online, BlockSuffixes: append([]string(nil), blocks...),
			Stubs: []Stub{{Host: "s3.amazonaws.com", Path: "/b", Body: []byte("stub")}},
		}
		if online {
			cfg.DialUpstream = dialTo(up.Listener.Addr().String())
		} else {
			cfg.DialUpstream = refuseDial(t)
		}
		p, pu := testProxy(t, cfg)
		c := client(t, p, pu)
		for _, u := range []string{
			"https://s3.amazonaws.com/b", "https://sqs.eu-west-1.amazonaws.com/", "http://dynamodb.us-east-1.amazonaws.com/",
			"https://s3.cn-north-1.amazonaws.com.cn/", "https://sts.api.aws/", "https://x.lambda-url.us-east-1.on.aws/",
			"https://AMAZONAWS.COM./",
		} {
			resp, body := get(t, c, http.MethodGet, u, nil, nil)
			if resp.StatusCode != http.StatusBadGateway || body != BlockedAWSMessage+"\n" {
				t.Errorf("online=%v %s: %d %q", online, u, resp.StatusCode, body)
			}
		}
		flows := waitFlows(t, p, 7)
		for _, f := range flows {
			if f.Outcome != OutcomeRefused || f.Status != http.StatusBadGateway {
				t.Errorf("online=%v flow %s = %s %d", online, f.URL, f.Outcome, f.Status)
			}
		}
		// A look-alike host is not AWS.
		resp, body := get(t, c, http.MethodGet, "http://notamazonaws.com/x", nil, nil)
		if online && (resp.StatusCode != 200 || body != "upstream notamazonaws.com") {
			t.Errorf("online look-alike: %d %q", resp.StatusCode, body)
		}
		if !online && !strings.Contains(body, "offline sandbox") {
			t.Errorf("offline look-alike: %d %q", resp.StatusCode, body)
		}
	}
}

// TestAWSTapForwardsUnchanged keeps the Host header, path, query, headers
// and body, and refuses the emulator's admin paths.
func TestAWSTapForwardsUnchanged(t *testing.T) {
	var got *http.Request
	var gotBody string
	emu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, gotBody = r, string(b)
		w.Header().Set("X-Amz-Request-Id", "req1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer emu.Close()
	tap, err := NewAWSTap(emu.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = tap.Serve(ln) }()
	defer func() { _ = tap.Close() }()

	req, err := http.NewRequest(http.MethodPut, "http://"+ln.Addr().String()+"/bucket/key%2Fa?x-id=PutObject", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "aws:4566"
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20261118/us-east-1/s3/aws4_request")
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Amz-Request-Id") != "req1" {
		t.Fatalf("tap answer %d %v", resp.StatusCode, resp.Header)
	}
	if got.Host != "aws:4566" || got.URL.RawQuery != "x-id=PutObject" || got.URL.EscapedPath() != "/bucket/key%2Fa" ||
		got.Header.Get("Authorization") == "" || got.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" || gotBody != "body" {
		t.Fatalf("emulator saw host %q path %q query %q headers %v body %q", got.Host, got.URL.EscapedPath(), got.URL.RawQuery, got.Header, gotBody)
	}
	for _, p := range []string{"/_ministack/config", "/_ministack/lambda-layers", "/_ministack/rds/x", "/_ministack/transfer/x", "/_ministack/eks-auth"} {
		got = nil
		resp, err := http.Post("http://"+ln.Addr().String()+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || got != nil {
			t.Errorf("%s: %d, reached emulator %v", p, resp.StatusCode, got != nil)
		}
	}
	resp, err = http.Get("http://" + ln.Addr().String() + "/_ministack/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health through the tap: %d", resp.StatusCode)
	}
}
