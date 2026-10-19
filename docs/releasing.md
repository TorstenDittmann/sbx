# Releasing

Pushing a tag `vX.Y.Z` (for example `v0.2.0` or `v0.2.0-rc.1`) runs `.github/workflows/release.yml`.

The workflow:

1. Refuses the tag unless `cmd/sbx-agent/main.go` is in that commit. The image has to contain the agent.
2. Builds static archives with GoReleaser and uploads them to a GitHub release, with `checksums.txt`, `install.sh`, and generated notes.
3. Publishes `ghcr.io/torstendittmann/sbx` for `linux/amd64` and `linux/arm64`, tagged with the version, the git tag, and `latest` on a final release.
4. Sets `sdk/typescript` to the same version and runs `npm publish --provenance` for `@torstendittmann/sbx`. Prereleases use the dist-tag `next` and do not move `latest`.

`sbx` archives are linux and darwin, amd64 and arm64. `sbx-agent` archives are linux only. `sbx version` prints the version, commit, and build time from `-ldflags`.

Pull requests run the same GoReleaser config as a snapshot. The snapshot does not create a release, push an image, or publish to npm. Until `cmd/sbx-agent` is on the branch, the snapshot sets `SBX_SKIP_AGENT=true` and builds `sbx` only. A tag never takes that shortcut.

## What the owner configures

No extra token is required for GitHub releases or GHCR. The workflow uses `GITHUB_TOKEN` with `contents: write` and `packages: write`.

1. **Actions permissions.** Repository settings, Actions, General, Workflow permissions: read and write. The workflow asks for those permissions itself, but an organization cap of read-only blocks the release job.
2. **`NPM_TOKEN` repository secret.** An npm automation token (or a granular token that can publish `@torstendittmann/sbx` and can run from CI without an interactive 2FA prompt). The secret name is `NPM_TOKEN`. The npm user `torstendittmann` has to exist first; that scope was unused when this pipeline was added. The publish job fails with an explicit error when the secret is missing, so a pull request stays green without it.
3. **GHCR visibility, after the first publish.** Open the package `ghcr.io/torstendittmann/sbx`. The image label `org.opencontainers.image.source` links it to this repository. If the package is private, change it to public so `docker pull` works without a login.

Do not commit the npm token. Do not add a personal access token for GHCR.

## First release

Merge the sbx-agent pull request first, then merge the release-pipeline pull request, so `main` contains `cmd/sbx-agent` and this workflow. Add `NPM_TOKEN`. Then:

```bash
git checkout main
git pull
git tag v0.2.0
git push origin v0.2.0
```

The source tree can stay on its checked-in version string. The tag is what GoReleaser and npm publish.

After the workflow is green:

```bash
sbx version
docker pull ghcr.io/torstendittmann/sbx:0.2.0
npm view @torstendittmann/sbx version
```

If the package was created private, make it public before the `docker pull` from a machine that is not logged in.
