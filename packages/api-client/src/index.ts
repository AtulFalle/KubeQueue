import type { components, operations } from './generated';

export type { components, operations, paths } from './generated';

export const apiContractVersion = '1.0.0' as const;

export type JobState = components['schemas']['JobState'];
export type Job = components['schemas']['Job'];
export type JobList = components['schemas']['JobList'];
export type JobPage = components['schemas']['JobPage'];
export type JobFacets = components['schemas']['JobFacets'];
export type JobEvent = components['schemas']['JobEvent'];
export type JobEventPage = components['schemas']['JobEventPage'];
export type JobManifest = components['schemas']['JobManifest'];
export type ErrorDetails = components['schemas']['ErrorResponse']['error']['details'];
export type SystemStatus = components['schemas']['SystemStatus'];
export type SupportDiagnostics = components['schemas']['SupportDiagnostics'];
export type BrowserSession = components['schemas']['BrowserSession'];
export type CreatedBrowserSession = components['schemas']['CreatedBrowserSession'];
export type OAuthLoginAttempt = components['schemas']['OAuthLoginAttempt'];
export type ConsumedOAuthLoginAttempt = components['schemas']['ConsumedOAuthLoginAttempt'];
export type SetupStatus = components['schemas']['SetupStatus'];
export type SetupClaimRequest = components['schemas']['SetupClaimRequest'];
export type SetupClaim = components['schemas']['SetupClaim'];
export type SetupRecovery = components['schemas']['SetupRecovery'];
export type Permission = components['schemas']['Permission'];
export type CurrentAccess = components['schemas']['CurrentAccess'];
export type AuditEvent = components['schemas']['AuditEvent'];
export type AuditEventPage = components['schemas']['AuditEventPage'];
export type AuditEventFilters = Omit<
  NonNullable<operations['searchAuditEvents']['parameters']['query']>,
  'installationId'
>;
export type Project = components['schemas']['Project'];
export type ProjectPage = components['schemas']['ProjectPage'];
export type CreateProject = components['schemas']['CreateProject'];
export type NamespaceBinding = components['schemas']['NamespaceBinding'];
export type NamespaceBindingPage = components['schemas']['NamespaceBindingPage'];
export type CreateNamespaceBinding = components['schemas']['CreateNamespaceBinding'];
export type AdmissionPolicy = components['schemas']['AdmissionPolicy'];
export type UpdateAdmissionPolicy = components['schemas']['UpdateAdmissionPolicy'];
export type ProjectAdmissionSettings = components['schemas']['ProjectAdmissionSettings'];
export type UpdateProjectAdmissionSettings =
  components['schemas']['UpdateProjectAdmissionSettings'];
export type QuotaCounters = components['schemas']['QuotaCounters'];
export type AdmissionDecisionPage = components['schemas']['AdmissionDecisionPage'];
export type Team = components['schemas']['Team'];
export type TeamPage = components['schemas']['TeamPage'];
export type CreateTeam = components['schemas']['CreateTeam'];
export type TeamMembership = components['schemas']['TeamMembership'];
export type TeamMembershipPage = components['schemas']['TeamMembershipPage'];
export type RoleDefinition = components['schemas']['RoleDefinition'];
export type RoleDefinitionPage = components['schemas']['RoleDefinitionPage'];
export type CreateRoleDefinition = components['schemas']['CreateRoleDefinition'];
export type UpdateRoleDefinition = components['schemas']['UpdateRoleDefinition'];
export type RoleBinding = components['schemas']['RoleBinding'];
export type RoleBindingPage = components['schemas']['RoleBindingPage'];
export type CreateRoleBinding = components['schemas']['CreateRoleBinding'];
export type ServiceAccount = components['schemas']['ServiceAccount'];
export type ServiceAccountPage = components['schemas']['ServiceAccountPage'];
export type CreateServiceAccount = components['schemas']['CreateServiceAccount'];
export type BindServiceAccountOIDCIdentity =
  components['schemas']['BindServiceAccountOIDCIdentity'];
export type ServiceAccountCredential = components['schemas']['ServiceAccountCredential'];
export type ServiceAccountCredentialPage = components['schemas']['ServiceAccountCredentialPage'];
export type IssueServiceAccountCredential = components['schemas']['IssueServiceAccountCredential'];
export type IssuedServiceAccountCredential =
  components['schemas']['IssuedServiceAccountCredential'];
export type RotateServiceAccountCredential =
  components['schemas']['RotateServiceAccountCredential'];
export type RotatedServiceAccountCredential =
  components['schemas']['RotatedServiceAccountCredential'];
type GeneratedCreateJob = components['schemas']['CreateJob'];
export type CreateJob = Omit<GeneratedCreateJob, 'priority'> & {
  priority?: GeneratedCreateJob['priority'];
};
export type JobAction = operations['commandJob']['parameters']['path']['action'];
export type JobFilters = NonNullable<operations['listJobs']['parameters']['query']>;
export type JobEventFilters = NonNullable<operations['listJobEvents']['parameters']['query']>;
export type QueueUpdate =
  operations['updateQueuedJob']['requestBody']['content']['application/json'];

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly requestId?: string,
    public readonly details: ErrorDetails = {},
  ) {
    super(message);
  }
}

export type Versioned<T> = {
  value: T;
  etag: string;
};

export class KubeQueueClient {
  constructor(
    private readonly baseUrl = '/api/v1',
    private readonly token?: string,
    private readonly authenticationScheme: 'Bearer' | 'Session' = 'Bearer',
  ) {}

  listJobs(filters: JobFilters = {}) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) {
      if (value !== undefined && value !== '') query.set(key, String(value));
    }
    return this.request<JobPage>(`/jobs?${query}`);
  }

  getSystemStatus() {
    return this.request<SystemStatus>('/system/status');
  }

  getSupportDiagnostics() {
    return this.request<SupportDiagnostics>('/support/diagnostics');
  }

  getSetupStatus() {
    return this.request<SetupStatus>('/setup/status');
  }

  claimSetup(input: SetupClaimRequest) {
    return this.request<SetupClaim>('/setup/claim', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  }

  getSetupRecovery() {
    return this.request<SetupRecovery>('/setup/recovery');
  }

  getCurrentAccess() {
    return this.request<CurrentAccess>('/access/me');
  }

  searchAuditEvents(installationId: string, filters: AuditEventFilters = {}) {
    return this.auditEventPage('/audit/events', installationId, filters);
  }

  getAuditEvent(installationId: string, auditEventId: string) {
    const query = new URLSearchParams({ installationId });
    return this.request<AuditEvent>(`/audit/events/${encodeURIComponent(auditEventId)}?${query}`);
  }

  exportAuditEvents(installationId: string, filters: AuditEventFilters = {}) {
    return this.auditEventPage('/audit/export', installationId, filters);
  }

  listProjects(cursor?: string) {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<ProjectPage>(`/projects?${query}`);
  }

  createProject(input: CreateProject) {
    return this.request<Project>('/projects', { method: 'POST', body: JSON.stringify(input) });
  }

  getProject(projectId: string) {
    return this.request<Project>(`/projects/${encodeURIComponent(projectId)}`);
  }

  listProjectNamespaceBindings(projectId: string, cursor?: string) {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<NamespaceBindingPage>(
      `/projects/${encodeURIComponent(projectId)}/namespace-bindings?${query}`,
    );
  }

  createProjectNamespaceBinding(projectId: string, input: CreateNamespaceBinding) {
    return this.request<NamespaceBinding>(
      `/projects/${encodeURIComponent(projectId)}/namespace-bindings`,
      { method: 'POST', body: JSON.stringify(input) },
    );
  }

  reassignProjectNamespaceBinding(projectId: string, namespace: string) {
    return this.request<NamespaceBinding>(
      `/projects/${encodeURIComponent(projectId)}/namespace-bindings/${encodeURIComponent(namespace)}`,
      { method: 'PUT' },
    );
  }

  removeProjectNamespaceBinding(projectId: string, namespace: string) {
    return this.requestVoid(
      `/projects/${encodeURIComponent(projectId)}/namespace-bindings/${encodeURIComponent(namespace)}`,
      { method: 'DELETE' },
    );
  }

  getInstallationAdmissionPolicy() {
    return this.requestVersioned<AdmissionPolicy>('/installation/admission-policy');
  }

  updateInstallationAdmissionPolicy(etag: string, input: UpdateAdmissionPolicy) {
    return this.requestVersioned<AdmissionPolicy>('/installation/admission-policy', {
      method: 'PATCH',
      headers: { 'If-Match': etag },
      body: JSON.stringify(input),
    });
  }

  getProjectAdmissionSettings(projectId: string) {
    return this.requestVersioned<ProjectAdmissionSettings>(
      `/projects/${encodeURIComponent(projectId)}/admission-settings`,
    );
  }

  updateProjectAdmissionSettings(
    projectId: string,
    etag: string,
    input: UpdateProjectAdmissionSettings,
  ) {
    return this.requestVersioned<ProjectAdmissionSettings>(
      `/projects/${encodeURIComponent(projectId)}/admission-settings`,
      {
        method: 'PATCH',
        headers: { 'If-Match': etag },
        body: JSON.stringify(input),
      },
    );
  }

  getProjectQuotaUsage(projectId: string) {
    return this.request<QuotaCounters>(`/projects/${encodeURIComponent(projectId)}/quota-usage`);
  }

  listProjectAdmissionDecisions(projectId: string, cursor?: string) {
    const query = new URLSearchParams({ limit: '50' });
    if (cursor) query.set('cursor', cursor);
    return this.request<AdmissionDecisionPage>(
      `/projects/${encodeURIComponent(projectId)}/admission-decisions?${query}`,
    );
  }

  listTeams() {
    return this.request<TeamPage>('/teams?limit=100');
  }

  createTeam(input: CreateTeam) {
    return this.request<Team>('/teams', { method: 'POST', body: JSON.stringify(input) });
  }

  listTeamMemberships(teamId: string) {
    return this.request<TeamMembershipPage>(
      `/teams/${encodeURIComponent(teamId)}/members?limit=100`,
    );
  }

  addTeamMembership(teamId: string, principalId: string) {
    return this.request<TeamMembership>(`/teams/${encodeURIComponent(teamId)}/members`, {
      method: 'POST',
      body: JSON.stringify({ principalId }),
    });
  }

  removeTeamMembership(teamId: string, principalId: string) {
    return this.requestVoid(
      `/teams/${encodeURIComponent(teamId)}/members/${encodeURIComponent(principalId)}`,
      { method: 'DELETE' },
    );
  }

  listRoleDefinitions() {
    return this.request<RoleDefinitionPage>('/role-definitions?limit=100');
  }

  createRoleDefinition(input: CreateRoleDefinition) {
    return this.request<RoleDefinition>('/role-definitions', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  }

  updateRoleDefinition(id: string, revision: number, input: UpdateRoleDefinition) {
    return this.requestVersioned<RoleDefinition>(`/role-definitions/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'If-Match': `"${revision}"` },
      body: JSON.stringify(input),
    }).then(({ value }) => value);
  }

  listRoleBindings() {
    return this.request<RoleBindingPage>('/role-bindings?limit=100');
  }

  createRoleBinding(input: CreateRoleBinding) {
    return this.request<RoleBinding>('/role-bindings', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  }

  deleteRoleBinding(id: string) {
    return this.requestVoid(`/role-bindings/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  listServiceAccounts() {
    return this.request<ServiceAccountPage>('/service-accounts?limit=100');
  }

  createServiceAccount(input: CreateServiceAccount) {
    return this.request<ServiceAccount>('/service-accounts', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  }

  bindServiceAccountOIDCIdentity(serviceAccountId: string, input: BindServiceAccountOIDCIdentity) {
    return this.request<ServiceAccount>(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/oidc-identity`,
      { method: 'PUT', body: JSON.stringify(input) },
    );
  }

  removeServiceAccountOIDCIdentity(serviceAccountId: string) {
    return this.requestVoid(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/oidc-identity`,
      { method: 'DELETE' },
    );
  }

  listServiceAccountCredentials(serviceAccountId: string) {
    return this.request<ServiceAccountCredentialPage>(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/credentials?limit=100`,
    );
  }

  issueServiceAccountCredential(serviceAccountId: string, input: IssueServiceAccountCredential) {
    return this.request<IssuedServiceAccountCredential>(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/credentials`,
      { method: 'POST', body: JSON.stringify(input) },
    );
  }

  rotateServiceAccountCredential(
    serviceAccountId: string,
    credentialId: string,
    input: RotateServiceAccountCredential,
  ) {
    return this.request<RotatedServiceAccountCredential>(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/credentials/${encodeURIComponent(credentialId)}/rotate`,
      { method: 'POST', body: JSON.stringify(input) },
    );
  }

  revokeServiceAccountCredential(serviceAccountId: string, credentialId: string) {
    return this.requestVoid(
      `/service-accounts/${encodeURIComponent(serviceAccountId)}/credentials/${encodeURIComponent(credentialId)}`,
      { method: 'DELETE' },
    );
  }

  getJobFacets() {
    return this.request<JobFacets>('/jobs/facets');
  }

  getQueue() {
    return this.request<JobList>('/queue');
  }

  getJob(id: string) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}`);
  }

  getJobManifest(id: string) {
    return this.request<JobManifest>(`/jobs/${encodeURIComponent(id)}/manifest`);
  }

  listJobEvents(id: string, filters: JobEventFilters = {}) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) {
      if (value !== undefined && value !== '') query.set(key, String(value));
    }
    return this.request<JobEventPage>(`/jobs/${encodeURIComponent(id)}/events?${query}`);
  }

  createJob(input: CreateJob, idempotencyKey = globalThis.crypto.randomUUID()) {
    return this.request<Job>('/jobs', {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify(input),
    });
  }

  command(id: string, action: JobAction) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}/actions/${action}`, {
      method: 'POST',
    });
  }

  updateQueue(id: string, input: QueueUpdate) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}/queue`, {
      method: 'PATCH',
      body: JSON.stringify(input),
    });
  }

  reorder(jobIds: string[], version = 0) {
    return this.request<{ version: number }>('/queue/order', {
      method: 'PUT',
      body: JSON.stringify({ jobIds, version }),
    });
  }

  reorderProject(projectId: string, jobIds: string[], version = 0) {
    return this.request<{ version: number }>(
      `/projects/${encodeURIComponent(projectId)}/queue/order`,
      {
        method: 'PUT',
        body: JSON.stringify({ jobIds, version }),
      },
    );
  }

  eventsUrl() {
    return `${this.baseUrl}/events`;
  }

  private auditEventPage(
    path: '/audit/events' | '/audit/export',
    installationId: string,
    filters: AuditEventFilters,
  ) {
    const query = new URLSearchParams({ installationId });
    for (const [key, value] of Object.entries(filters)) {
      if (value !== undefined && value !== '') query.set(key, String(value));
    }
    return this.request<AuditEventPage>(`${path}?${query}`);
  }

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await this.fetch(path, init);
    return (await response.json()) as T;
  }

  private async requestVersioned<T>(path: string, init: RequestInit = {}): Promise<Versioned<T>> {
    const response = await this.fetch(path, init);
    const etag = response.headers.get('etag');
    if (!etag) throw new ApiError(502, 'MISSING_ETAG', 'Versioned response omitted ETag');
    return { value: (await response.json()) as T, etag };
  }

  private async requestVoid(path: string, init: RequestInit = {}): Promise<void> {
    await this.fetch(path, init);
  }

  private async fetch(path: string, init: RequestInit) {
    const csrfToken =
      typeof document !== 'undefined' && init.method && !['GET', 'HEAD'].includes(init.method)
        ? document.querySelector<HTMLMetaElement>('meta[name="kubequeue-csrf"]')?.content
        : undefined;
    const response = await fetch(`${this.baseUrl}${path}`, {
      ...init,
      cache: 'no-store',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...(this.token ? { Authorization: `${this.authenticationScheme} ${this.token}` } : {}),
        ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
        ...init.headers,
      },
    });
    if (!response.ok) {
      const payload = (await response.json().catch(() => undefined)) as
        | {
            requestId?: string;
            error?: { code?: string; message?: string; details?: ErrorDetails };
          }
        | undefined;
      throw new ApiError(
        response.status,
        payload?.error?.code ?? 'REQUEST_FAILED',
        payload?.error?.message ?? `Request failed with status ${response.status}`,
        payload?.requestId ?? response.headers.get('x-request-id') ?? undefined,
        payload?.error?.details ?? {},
      );
    }
    return response;
  }
}
