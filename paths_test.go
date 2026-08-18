package main

import "testing"

func TestPathWithin(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/Users/me/Desktop", "/Users/me/Desktop", true},
		{"/Users/me/Desktop/proj", "/Users/me/Desktop", true},
		// Raw prefix matching used to let a sibling directory through.
		{"/Users/me/Desktop-secret", "/Users/me/Desktop", false},
		{"/Users/me/Desktopx/proj", "/Users/me/Desktop", false},
		{"/Users/other", "/Users/me", false},
		// An empty root must never match everything.
		{"/etc/passwd", "", false},
		{"", "/Users/me", false},
		{"/etc", "/", true},
	}
	for _, tt := range tests {
		if got := pathWithin(tt.path, tt.dir); got != tt.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
		}
	}
}

func TestWithinAllowedIgnoresMissingRoots(t *testing.T) {
	// A configured root that does not exist must not open up the filesystem.
	allowed := []string{"/definitely/not/here"}
	if withinAllowed("/etc/passwd", allowed) {
		t.Error("missing allowed root should not permit arbitrary paths")
	}
	if !withinAllowed("/definitely/not/here/x", allowed) {
		t.Error("paths under a missing root should still resolve textually")
	}
}
