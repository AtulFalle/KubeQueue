# Security policy

KubeQueue v0.1.x is an experimental developer preview. It does not receive a production support
commitment or a guaranteed security-fix window. Security fixes are published in the latest preview
release when maintainers determine that a report affects the supported development branch.

Keep the initial installation cluster-private and complete guarded local-owner setup before adding
external access. Do not expose the preview through a public Service or Ingress without TLS and the
documented browser origin. OIDC is optional and can be configured later from Settings.

Report vulnerabilities privately through the repository's GitHub security advisory form. Do not
open a public issue with exploit details, credentials, cluster metadata, or affected workload
manifests.

Include the affected revision, impact, reproduction steps, and any suggested mitigation. Maintainers
will acknowledge the report as capacity permits and coordinate disclosure after a fix is available.
