package domain

import "testing"

func TestEnterpriseIdentityValuesValidateStableIdentifiers(t *testing.T) {
	t.Parallel()
	installation, err := NewInstallation("default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewProject("default", installation.ID, "Default")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewPrincipal("legacy_admin", installation.ID, "Legacy administrator")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewNamespaceBinding(project.ID, "batch-jobs")
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != "legacy_admin" || binding.ID != "default__batch-jobs" {
		t.Fatalf("principal = %q, binding = %q", principal.ID, binding.ID)
	}
	if !SubmissionSourceLegacyCompatibility.Valid() || SubmissionSource("unknown").Valid() {
		t.Fatal("submission source validation is inconsistent")
	}
}

func TestEnterpriseIdentityValuesRejectInvalidInput(t *testing.T) {
	t.Parallel()
	if _, err := NewInstallation("Not Stable", "Default"); err == nil {
		t.Fatal("NewInstallation() accepted invalid ID")
	}
	if _, err := NewProject("default", "", "Default"); err == nil {
		t.Fatal("NewProject() accepted empty installation ID")
	}
	if _, err := NewPrincipal("legacy_admin", "default", " "); err == nil {
		t.Fatal("NewPrincipal() accepted empty display name")
	}
	if _, err := NewNamespaceBinding("default", "Not_A_Namespace"); err == nil {
		t.Fatal("NewNamespaceBinding() accepted invalid namespace")
	}
}
