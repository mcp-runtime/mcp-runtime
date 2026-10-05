
# Supply Chain Audit

## Overview

Use this skill to audit everything that crosses the trust boundary into the
build: Go modules, container base images, image signatures, SBOMs, and
GitHub Actions. Scope intentionally excludes runtime authn/z, RBAC, and
cluster posture — those live in `security-audit` and
`security-audit`.

Findings use the shared template at
`../_shared/FINDINGS-TEMPLATE.md`.

## Step 1 — Inventory the build inputs

- Go modules: `go list -m -json all > /tmp/go-mods.json` from repo root
  and from each `services/<name>/` with its own `go.mod`.
- Images built by this repo: collect Dockerfile paths.
  ```sh
  find . -name 'Dockerfile*' -not -path './inspirations/*' -not -path './node_modules/*'
  ```
- GitHub Actions workflows: `ls .github/workflows/`.
- Reusable composite actions: `find . -path '*/.github/actions/*' -name action.yml -o -name action.yaml`.
- Pre-commit hook versions: `.pre-commit-config.yaml`.

State the inventory in the report so a future audit knows the surface.

## Step 2 — Vulnerable Go dependencies

`govulncheck` reports CVEs the binary actually reaches, not just modules
in the graph. Run from every Go module root:

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest

govulncheck ./...
for d in services/*/; do
  [ -f "$d/go.mod" ] && (cd "$d" && govulncheck ./...)
done
```

Report each `Vulnerability` block as a finding with severity drawn from the
upstream advisory and the `CallStack` field as exploitability evidence.

## Step 3 — Container image scans

Mirror `.github/workflows/security-trivy.yaml` locally. Build every image
the repo produces and scan it as an image (filesystem scans miss base-image
CVEs). Pin the same Trivy action version (currently `0.36.0` →
`aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25`):

```sh
docker build --pull -f Dockerfile.operator -t mcp-runtime-operator:audit .
docker build --pull -f services/platform-api/Dockerfile -t mcp-platform-api:audit .
docker build --pull -f services/analytics-api/Dockerfile -t mcp-analytics-api:audit .
docker build --pull -f services/runtime-api/Dockerfile -t mcp-runtime-api:audit .
docker build --pull -f services/platform-api/Dockerfile -t mcp-platform-api:audit .
docker build --pull -f services/runtime-api/Dockerfile -t mcp-runtime-api:audit .
docker build --pull -f services/analytics-api/Dockerfile -t mcp-analytics-api:audit .
docker build --pull -f services/ui/Dockerfile -t mcp-ui:audit .
docker build --pull -f services/ingest/Dockerfile -t mcp-ingest:audit .
docker build --pull -f services/processor/Dockerfile -t mcp-processor:audit .
docker build --pull -f services/mcp-gateway/Dockerfile -t mcp-gateway:audit .
docker build --pull -f services/oauth-server/Dockerfile -t mcp-oauth-server:audit .

# Or use the repo helper — NOTE: as of this audit hack/trivy-platform-images.sh
# does not include oauth-server (its SERVICE_DOCKERFILES map stops at
# mcp-gateway), so it under-covers relative to the CI matrix below; run the
# manual oauth-server build/scan separately until the script is fixed.
bash hack/trivy-platform-images.sh

for img in mcp-runtime-operator:audit mcp-platform-api:audit mcp-runtime-api:audit \
           mcp-analytics-api:audit mcp-ui:audit \
           mcp-ingest:audit mcp-processor:audit \
           mcp-gateway:audit mcp-oauth-server:audit; do
  trivy image --exit-code 0 --severity CRITICAL,HIGH --ignore-unfixed \
              --vuln-type os,library --format table "$img"
done
```

For each image, also confirm:

- Distroless or static base where applicable; no shell unless explicitly
  needed.
- Non-root user in the final stage (`USER` directive present, UID ≠ 0).
- `HEALTHCHECK` either present or intentionally absent and documented.
- No `ADD` of remote URLs (use `COPY` with hashed assets, or
  `RUN curl ... | sha256sum -c` style).
- Build args do not bake secrets into layers (`docker history` shows no
  `--build-arg` token leakage).

Each gap is a finding (severity per the rubric).

## Step 4 — SBOM generation and diff

Match the CI step using `anchore/sbom-action` (currently
`@3ad7283483fc7af8ff2b4ea19663c2d5ca935e26` v0.24.2 — verify against
`.github/workflows/ci.yaml`, which is the pin of record; other workflows
should match it). Locally run `syft`:

```sh
go install github.com/anchore/syft/cmd/syft@latest

for img in mcp-runtime-operator:audit mcp-platform-api:audit mcp-runtime-api:audit \
           mcp-analytics-api:audit mcp-ui:audit mcp-ingest:audit \
           mcp-processor:audit mcp-gateway:audit \
           mcp-oauth-server:audit; do
  out=$(echo "$img" | tr ':/' '__').spdx.json
  syft "$img" -o spdx-json="/tmp/$out"
done
```

When auditing a release, diff against the previous release's SBOM
(downloaded from the prior `operator-image-sbom` artifact) and call out
new components, new transitive deps, and any drop in pinning quality.

## Step 5 — Image signatures (cosign)

If the repo signs images (check `.github/workflows/` for cosign / Sigstore
steps):

- Verify against the public key or keyless OIDC identity:
  ```sh
  cosign verify --certificate-identity-regexp '.*' \
                --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
                <registry>/<repo>:<tag>
  ```
- Confirm a verifying admission policy (Kyverno / connaisseur / sigstore
  policy controller) exists in `k8s/` or `config/`. If signing is in CI
  but unverified at admission, that gap is a Medium finding.

If no signing is configured, that absence is at least a Low finding for an
alpha project, and Medium when targeting release.

## Step 6 — GitHub Actions hardening

For every workflow under `.github/workflows/`:

- **Action pinning**: every `uses:` should reference a 40-char commit SHA,
  not a tag like `@v4` or `@main`. Mismatches are findings (Medium when
  the action handles secrets, otherwise Low).
  ```sh
  grep -RIn 'uses:' .github/workflows/ | grep -Ev '@[0-9a-f]{40}'
  ```
- **Permissions**: each workflow has top-level `permissions:` set to the
  minimum (often `contents: read`). Job-level overrides only where needed.
  Implicit (default) write tokens on PR-triggered workflows are a finding.
- **`pull_request_target` + checkout of HEAD**: classic pwn-request. Any
  workflow that combines `on: pull_request_target` with
  `actions/checkout` of `${{ github.event.pull_request.head.sha }}` (or
  `head.ref`) and uses repository or deploy secrets is **Critical**.
- **Secret usage**: list which workflows reference which secrets:
  ```sh
  grep -RIn 'secrets\.' .github/workflows/
  ```
  For each, confirm the workflow trigger does not allow a forked PR to
  read the secret.
- **Composite/reusable actions**: same pinning rules apply, plus inputs
  must validate before passing to `run:` to prevent shell injection from
  PR titles or branch names.
- **Self-hosted runners**: if any, confirm they are ephemeral and not
  shared with public PR workflows.

## Step 7 — Dependency review and licensing

- Compare current `go.mod`/`go.sum` against the merge base:
  ```sh
  git diff origin/main...HEAD -- '**/go.mod' '**/go.sum'
  ```
- New or upgraded modules: confirm the upstream is maintained (last commit
  date), the license is acceptable, and there are no known typosquats.
- License audit: Trivy's `--scanners license` flag covers common cases;
  for stricter audits run
  ```sh
  go install github.com/google/go-licenses@latest
  go-licenses report ./... --include_tests=false
  ```
  Flag GPL/AGPL where the project intends Apache-2.0, and flag UNKNOWN
  licenses as Medium until manually triaged.

## Step 8 — Pre-commit, gitleaks, and secret hygiene

- `pre-commit run --all-files` runs the pinned hooks; any drift in
  `.pre-commit-config.yaml` versions vs CI is a Low finding.
- `gitleaks detect --source . --config .gitleaks.toml --redact` against
  the working tree.
- `gitleaks detect --log-opts="--all"` against full history (slow; run
  for release audits). Any historical leak triggers a credential
  rotation, not just removal.

## Step 9 — Reusable artifacts and provenance

- If CI publishes images, verify the registry uses immutable tags or
  digest-based deploys.
- If SLSA / in-toto attestations are produced, verify them with
  `slsa-verifier` or `cosign verify-attestation`.
- If none exist, recommend at least signed SBOMs for release builds.

## Step 10 — Report

Use `../_shared/FINDINGS-TEMPLATE.md`. For supply-chain audits,
include in the Summary section:

- Total Go modules and direct vs transitive count.
- Number of images scanned and total CVEs by severity.
- Workflow count, % pinned by SHA, list of unpinned actions.
- SBOM diff vs previous release if applicable.

Cross-reference findings against `security-audit` (e.g., a CVE in
the gateway proxy is also a runtime finding) so the same root cause is not
counted twice.
