# API contract

`openapi/openapi.yaml` is the source of truth for KubeQueue's public HTTP API.

## Local documentation

Start the development stack and open <http://localhost:8081>. Swagger UI reloads the mounted
contract when the page is refreshed.

## Contract workflow

1. Update the OpenAPI document before changing public API behavior.
2. Run `pnpm nx run api-contract:lint`.
3. Implement the matching Go handler and application use case.
4. Regenerate the TypeScript client after a generator target is introduced.

Do not expose Kubernetes API objects directly. Use stable product schemas, operation IDs, UTC
timestamps, and the shared error envelope once those schemas are defined.
