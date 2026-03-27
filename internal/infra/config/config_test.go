package config

import "testing"

func TestPersonalReposEnabled(t *testing.T) {
	if !PersonalReposEnabled(nil) {
		t.Fatal("nil GitConfig should default to personal repos on")
	}
	if !PersonalReposEnabled(&GitConfig{}) {
		t.Fatal("empty GitConfig (nil pointer) should default to personal repos on")
	}
	f := false
	if PersonalReposEnabled(&GitConfig{IncludePersonal: &f}) {
		t.Fatal("explicit false should disable personal repos")
	}
	tr := true
	if !PersonalReposEnabled(&GitConfig{IncludePersonal: &tr}) {
		t.Fatal("explicit true should enable personal repos")
	}
}
