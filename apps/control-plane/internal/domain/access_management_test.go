package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCustomRoleRejectsNonDelegableAndUnknownPermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		permission Permission
		want       error
	}{
		{name: "owner recovery", permission: PermissionInternalAll, want: ErrNonDelegablePermission},
		{name: "unknown", permission: Permission("unknown.permission"), want: ErrInvalidAccessChange},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRoleDefinition(
				"custom_reader", "default", "Custom reader", RoleScopeProject,
				[]Permission{test.permission}, time.Now(),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewRoleDefinition() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCustomRoleStartsAtFirstRevisionWithCanonicalPermissions(t *testing.T) {
	t.Parallel()
	now := time.Now()
	role, err := NewRoleDefinition(
		"custom_operator", "default", "Custom operator", RoleScopeProject,
		[]Permission{PermissionJobsRead, PermissionJobsList}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if role.Revision != 1 {
		t.Fatalf("revision = %d, want 1", role.Revision)
	}
	if len(role.Permissions) != 2 ||
		role.Permissions[0] != PermissionJobsList ||
		role.Permissions[1] != PermissionJobsRead {
		t.Fatalf("permissions = %#v, want canonical order", role.Permissions)
	}
}

func TestAccessPageAndBindingAreBoundedAndScoped(t *testing.T) {
	t.Parallel()
	if err := (AccessPage{Limit: MaxAccessPageSize + 1}).Validate(); !errors.Is(err, ErrInvalidAccessChange) {
		t.Fatalf("oversized page error = %v", err)
	}
	page, err := (AccessPage{After: " project_one "}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != DefaultAccessPageSize || page.After != "project_one" {
		t.Fatalf("normalized page = %#v", page)
	}
	_, err = NewRoleBinding(
		"reader_binding", "default", "viewer", RoleScopeProject, "",
		BindingSubjectPrincipal, "user_one", "", time.Now(),
	)
	if !errors.Is(err, ErrInvalidAccessChange) {
		t.Fatalf("projectless binding error = %v", err)
	}
}
