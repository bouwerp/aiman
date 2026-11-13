package slack

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseArchiveLink(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ArchiveLink
	}{
		{"https://ws.slack.com/archives/C0B7EHJRC0Z/p1794563498596289", ArchiveLink{Channel: "C0B7EHJRC0Z", TS: "1794563498.596289"}},
		{"https://ws.slack.com/archives/C0B7EHJRC0Z/p1794564000123456?thread_ts=1794563498.596289&cid=C0B7EHJRC0Z",
			ArchiveLink{Channel: "C0B7EHJRC0Z", TS: "1794564000.123456", ThreadTS: "1794563498.596289"}},
		{"https://ws.enterprise.slack.com/archives/G012/", ArchiveLink{Channel: "G012"}},
	} {
		got, err := ParseArchiveLink(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"https://example.com/archives/C012/p1794563498596289",
		"https://ws.slack.com.evil.io/archives/C012/p1794563498596289",
		"https://ws.slack.com/archives/C012/p179456349859628",
		"https://ws.slack.com/archives/U012/p1794563498596289",
		"https://ws.slack.com/archives/C012/p1794563498596289?thread_ts=abc",
		"https://ws.slack.com/archives/C012/p1794563498596289?cid=C999",
		"ftp://ws.slack.com/archives/C012/p1794563498596289",
	} {
		if _, err := ParseArchiveLink(bad); !errors.Is(err, ErrNotArchiveLink) {
			t.Errorf("%s: err %v, want ErrNotArchiveLink", bad, err)
		}
	}
}

func TestMessageOmitsAbsentFields(t *testing.T) {
	b, err := json.Marshal(Message{Type: "message", User: "U1", Text: "hi", TS: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"message","user":"U1","text":"hi","ts":"1.0"}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}
