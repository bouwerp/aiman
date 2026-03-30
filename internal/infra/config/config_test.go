package config

import (
	"reflect"
	"testing"
)

func TestUniqueRemotes(t *testing.T) {
	in := []Remote{
		{Name: "a", Host: "h1", User: "u", Root: "/r"},
		{Name: "b", Host: "h1", User: "u", Root: "/r"},
		{Name: "c", Host: "h2", User: "u", Root: "/r"},
		{Name: "", Host: "", User: "", Root: ""},
	}
	got := UniqueRemotes(in)
	want := []Remote{
		{Name: "a", Host: "h1", User: "u", Root: "/r"},
		{Name: "c", Host: "h2", User: "u", Root: "/r"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
