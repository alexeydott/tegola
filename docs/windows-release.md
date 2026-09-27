# Windows release builds

Build from the committed source with Go matching `go.mod`, Node/npm and an
amd64 C compiler on PATH. CGO must be enabled to include GeoPackage support.
Run these PowerShell commands from the repository root; stop on any failed step.

```powershell
Push-Location ui
try {
    npm ci --ignore-scripts --no-audit --no-fund
    if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }
    npm run build
    if ($LASTEXITCODE -ne 0) { throw 'viewer build failed' }
} finally {
    Pop-Location
}
# Vite cleans dist; retain the tracked placeholder.
git restore -- ui/dist/.keep

$env:CGO_ENABLED = '1'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$revision = git rev-parse HEAD
$shortRevision = git rev-parse --short=8 HEAD
$branch = git branch --show-current
$version = "v0.21.0-fork.1+git.$shortRevision"
$flags = "-s -w -X github.com/go-spatial/tegola/internal/build.Version=$version -X github.com/go-spatial/tegola/internal/build.GitRevision=$revision -X github.com/go-spatial/tegola/internal/build.GitBranch=$branch"
go build -mod=vendor -trimpath -ldflags $flags -o tegola.exe ./cmd/tegola
if ($LASTEXITCODE -ne 0) { throw 'release build failed' }
.\tegola.exe version
Get-FileHash .\tegola.exe -Algorithm SHA256
```

`-trimpath` is a Go build flag; `-s -w` are linker flags that omit debug
symbols. The version's `+git` suffix identifies a commit build without claiming
a new numbered fork release. For a tagged release, supply that tag as the
version instead. `tegola.exe version` must report the expected source commit,
CGO/GeoPackage support, and a built viewer rather than `viewer not built`.

Run the CGO-off/on test matrix and lint before publishing; distinguish gated
database/cloud tests from actual live-service checks. The completion audit's
verified scope and remaining limitations are listed in [UPSTREAM.md](../UPSTREAM.md).
In particular, geographic PROJ definitions currently support WGS84 identity,
and SQL token scanning assumes the documented default backend string modes.

Commit messages in this fork must not contain `Co-authored-by` trailers.
Fetch and reconcile `origin/master`, inspect the outgoing commits, and push
`master` without force. Keep generated binaries, checksums, logs and local
datasets out of source commits. Building or pushing source does not create
a GitHub Release or a tag.
