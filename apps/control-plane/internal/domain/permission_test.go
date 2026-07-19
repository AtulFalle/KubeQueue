package domain

import (
	"slices"
	"testing"
)

func TestPermissionCatalogIsDeterministicAndSorted(t *testing.T) {
	t.Parallel()
	first := PermissionCatalog()
	second := PermissionCatalog()
	if !slices.Equal(first, second) {
		t.Fatalf("PermissionCatalog() changed order: %v != %v", first, second)
	}
	if !slices.IsSorted(first) {
		t.Fatalf("PermissionCatalog() = %v, want sorted permissions", first)
	}
}
