docker_build(
    'kubequeue-api',
    '.',
    dockerfile='apps/control-plane/Dockerfile',
    build_args={'TARGET': 'api'},
)
docker_build(
    'kubequeue-worker',
    '.',
    dockerfile='apps/control-plane/Dockerfile',
    build_args={'TARGET': 'worker'},
)
docker_build('kubequeue-web', '.', dockerfile='apps/web/Dockerfile')

k8s_yaml('deploy/kind/postgres.yaml')
k8s_yaml(blob('''
apiVersion: v1
kind: Secret
metadata:
  name: tilt-security
type: Opaque
stringData:
  session-digest-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
  credential-encryption-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
  bff-internal-key: development-only-bff-key-00000000
  service-account-digest-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
'''))
k8s_yaml(helm(
    'deploy/helm/kubequeue',
    name='kubequeue',
    set=[
        'database.url=postgres://kubequeue:kubequeue@postgres:5432/kubequeue?sslmode=disable',
        'security.existingSecret=tilt-security',
        'browser.publicURL=http://localhost:3000',
        'browser.origin=http://localhost:3000',
        'runtime.environment=development',
        'development.localAdminSeed=true',
    ],
))

k8s_resource('postgres')
k8s_resource('kubequeue-kubequeue-api', port_forwards='8080:8080')
k8s_resource('kubequeue-kubequeue-web', port_forwards='3000:3000')
