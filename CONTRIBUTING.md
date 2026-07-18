# Contributing

## Before coding

1. Read `docs/architecture.md` and relevant ADRs.
2. Keep changes small and owned by one layer.
3. Update the OpenAPI contract before implementing public API behavior.
4. Add an ADR for changes to system boundaries or durable consistency rules.

## Quality gates

Use Nx as the task entry point:

```bash
pnpm format:check
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

Run focused tasks while developing, for example:

```bash
pnpm nx run web:typecheck
pnpm nx run control-plane:test
pnpm nx run deploy:helm-template
```

Installing dependencies configures a pre-commit hook that runs the same changed-file formatting and
lint checks as CI. Run it directly with `pnpm check:changed -- --staged`.

Go code must pass `gofmt`, `go vet`, `golangci-lint`, and race-enabled tests. TypeScript must remain
strict and may not use `any` to bypass contract or state modeling.

CI uses Nx Cloud for remote task caching. Repository maintainers must configure a
`NX_CLOUD_ACCESS_TOKEN` Actions secret; keep personal tokens in an untracked `nx-cloud.env` file.

## Change discipline

- Include tests with behavior changes.
- Do not commit credentials, local databases, cluster state, or generated build output.
- Commit generated clients only with the contract change that produced them.
- Explain user-visible behavior and operational impact in pull requests.
- Prefer focused commits with imperative messages such as `build: establish Go linting`.

## Pull requests

Use `.github/pull_request_template.md` without removing sections. Check only validations that
actually completed, include command results, and mark irrelevant sections as `Not applicable` with
a reason. Cursor-generated commits and pull requests follow `.cursor/rules/contribution-workflow.mdc`.
