package domain

import "sort"

// Permission is a stable authorization capability. Permissions are enumerated;
// user-defined wildcard permissions are intentionally unsupported.
type Permission string

const (
	PermissionInternalAll   Permission = "internal.all"
	PermissionAuthenticated Permission = "internal.authenticated"

	PermissionJobsList         Permission = "jobs.list"
	PermissionJobsRead         Permission = "jobs.read"
	PermissionJobsManifestRead Permission = "jobs.manifest.read"
	PermissionJobsSubmit       Permission = "jobs.submit"
	PermissionJobsPause        Permission = "jobs.pause"
	PermissionJobsResume       Permission = "jobs.resume"
	PermissionJobsTerminate    Permission = "jobs.terminate"
	PermissionJobsRetry        Permission = "jobs.retry"
	PermissionJobsTakeControl  Permission = "jobs.take-control"
	PermissionJobsArchive      Permission = "jobs.archive"
	PermissionJobEventsRead    Permission = "job-events.read"
	PermissionEventStreamRead  Permission = "events.stream.read"

	PermissionQueueRead           Permission = "queue.read"
	PermissionQueueEntryUpdate    Permission = "queue.entry.update"
	PermissionQueueProjectReorder Permission = "queue.project.reorder"
	PermissionQueueGlobalReorder  Permission = "queue.global.reorder"

	PermissionProjectsManage          Permission = "projects.manage"
	PermissionNamespaceBindingsManage Permission = "namespace-bindings.manage"
	PermissionMembersRead             Permission = "members.read"
	PermissionMembersManage           Permission = "members.manage"
	PermissionRolesRead               Permission = "roles.read"
	PermissionRolesAssign             Permission = "roles.assign"
	PermissionRolesDefine             Permission = "roles.define"
	PermissionServiceAccountsManage   Permission = "service-accounts.manage"
	PermissionTokensManage            Permission = "tokens.manage"
	PermissionPoliciesRead            Permission = "policies.read"
	PermissionPoliciesManage          Permission = "policies.manage"
	PermissionQuotasManage            Permission = "quotas.manage"
	PermissionAuditRead               Permission = "audit.read"
	PermissionAuditExport             Permission = "audit.export"
	PermissionSystemStatusRead        Permission = "system.status.read"
	PermissionSupportDiagnosticsRead  Permission = "support.diagnostics.read"
)

var permissionCatalog = map[Permission]struct{}{
	PermissionInternalAll: {}, PermissionAuthenticated: {}, PermissionJobsList: {}, PermissionJobsRead: {},
	PermissionJobsManifestRead: {}, PermissionJobsSubmit: {}, PermissionJobsPause: {},
	PermissionJobsResume: {}, PermissionJobsTerminate: {}, PermissionJobsRetry: {},
	PermissionJobsTakeControl: {}, PermissionJobsArchive: {}, PermissionJobEventsRead: {},
	PermissionEventStreamRead: {}, PermissionQueueRead: {}, PermissionQueueEntryUpdate: {},
	PermissionQueueProjectReorder: {}, PermissionQueueGlobalReorder: {},
	PermissionProjectsManage: {}, PermissionNamespaceBindingsManage: {},
	PermissionMembersRead: {}, PermissionMembersManage: {}, PermissionRolesRead: {},
	PermissionRolesAssign: {}, PermissionRolesDefine: {}, PermissionServiceAccountsManage: {},
	PermissionTokensManage: {}, PermissionPoliciesRead: {}, PermissionPoliciesManage: {},
	PermissionQuotasManage: {}, PermissionAuditRead: {}, PermissionAuditExport: {},
	PermissionSystemStatusRead: {}, PermissionSupportDiagnosticsRead: {},
}

func (p Permission) Valid() bool {
	_, ok := permissionCatalog[p]
	return ok
}

func PermissionCatalog() []Permission {
	result := make([]Permission, 0, len(permissionCatalog))
	for permission := range permissionCatalog {
		result = append(result, permission)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}
