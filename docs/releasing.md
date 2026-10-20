# Releasing

Pushing a tag `vX.Y.Z` (for example `v0.2.0` or `v0.2.0-rc.1`) runs `.github/workflows/release.yml`.

The workflow:

1. Refuses the tag unless `cmd/sbx-agent/main.go` is in that commit. The image has to contain the agent.
2. Builds static archives with GoReleaser and uploads them to a GitHub release, with `checksums.txt`, `install.sh`, and generated notes.
3. Publishes `ghcr.io/torstendittmann/sbx` for `linux/amd64` and `linux/arm64`, tagged with the version, the git tag, and `latest` on a final release.
4. Sets `sdk/typescript` to the same version and runs `npm publish --provenance` for `@sbx.torsten.work/sbx` through npm trusted publishing, so no npm token is stored. Prereleases use the dist-tag `next` and do not move `latest`. If that version is already on npm, the job skips the publish and passes.

`sbx` archives are linux and darwin, amd64 and arm64. `sbx-agent` archives are linux only. `sbx version` prints the version, commit, and build time from `-ldflags`.

Pull requests run the same GoReleaser config as a snapshot. The snapshot does not create a release, push an image, or publish to npm. If `cmd/sbx-agent` is missing, the snapshot sets `SBX_SKIP_AGENT=true` and builds `sbx` only. A tag never takes that shortcut.

## What the owner configures

No extra token is required for GitHub releases or GHCR. The workflow uses `GITHUB_TOKEN` with `contents: write` and `packages: write`.

1. **Actions permissions.** Repository settings, Actions, General, Workflow permissions: read and write. The workflow asks for those permissions itself, but an organization cap of read-only blocks the release job.
2. **npm trusted publisher.** On npmjs.com, open `@sbx.torsten.work/sbx`, Settings, Trusted Publisher, GitHub Actions. Organization or user `TorstenDittmann`, repository `sbx`, workflow filename `release.yml`, no environment. npm only offers this on a package that exists, so the very first version is published by hand (see below). Once it is set, also choose "Require two-factor authentication and disallow tokens" under publishing access.
3. **GHCR visibility, after the first publish.** Open the package `ghcr.io/torstendittmann/sbx`. The image label `org.opencontainers.image.source` links it to this repository. If the package is private, change it to public so `docker pull` works without a login.

Do not add an npm token or a personal access token for GHCR.

## First release

`cmd/sbx-agent` is already on `main`. The package does not exist on npm until the first publish, and npm cannot register a trusted publisher before that. Publish the first version once from your machine, at the tagged commit:

```bash
git checkout v0.2.0
cd sdk/typescript
npm ci
npm publish --access public --provenance=false
```

npm asks for your 2FA code. Provenance is off for this one publish because it only works from CI. Then add the trusted publisher (step 2 above). Every later tag publishes from CI with provenance.

To tag a release:

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
npm view @sbx.torsten.work/sbx version
```

If the package was created private, make it public before the `docker pull` from a machine that is not logged in.
