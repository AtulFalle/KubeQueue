# Security policy

KubeQueue v0.1.x is an experimental developer preview. It does not receive a production support
commitment or a guaranteed security-fix window. Security fixes are published in the latest preview
release when maintainers determine that a report affects the supported development branch.

The preview dashboard is a single-administrator surface. Keep it cluster-private and access it
through authenticated Kubernetes port-forwarding. Do not expose it directly through a public
Service or Ingress. Use a strong, non-empty admin token supplied through a Kubernetes Secret.

Report vulnerabilities privately through the repository's GitHub security advisory form. Do not
open a public issue with exploit details, credentials, cluster metadata, or affected workload
manifests.

Include the affected revision, impact, reproduction steps, and any suggested mitigation. Maintainers
will acknowledge the report as capacity permits and coordinate disclosure after a fix is available.
