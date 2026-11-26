package artifact

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

func TestR2ListPagesAndDelete(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/b" || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "a/x/v/" {
				t.Errorf("list request %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			if r.URL.Query().Get("continuation-token") == "" {
				fmt.Fprint(w, `<ListBucketResult><Contents><Key>a/x/v/1/index.html</Key></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>t2</NextContinuationToken></ListBucketResult>`)
				return
			}
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>a/x/v/2/index.html</Key></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`)
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			if r.URL.Path == "/b/gone" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	r2 := NewR2(srv.URL, "b", awssig.Credentials{AccessKeyID: "K", SecretAccessKey: "S"}, srv.Client())
	keys, err := r2.List(context.Background(), "a/x/v/")
	if err != nil || !slices.Equal(keys, []string{"a/x/v/1/index.html", "a/x/v/2/index.html"}) {
		t.Fatalf("list %v %v", keys, err)
	}
	if err := r2.Delete(context.Background(), []string{"a/x/v/1/index.html", "gone"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []string{"/b/a/x/v/1/index.html", "/b/gone"}) {
		t.Fatalf("deleted %v", deleted)
	}
}
