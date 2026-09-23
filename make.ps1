<#
.SYNOPSIS
  Windows equivalent of the Makefile (PowerShell 5.1+ ships with Windows, no `make` needed).

.EXAMPLE
  .\make.cmd test
  .\make.ps1 devlocal
  .\make.ps1 help
#>
param(
    [Parameter(Position = 0)][string]$Target = 'help'
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$Services = 'graph', 'registry', 'engine', 'modelgw', 'gateway', 'mcp', 'iam', 'goap-dev', 'goap-runner'
$Compose = @('compose', '-f', 'deploy/compose/docker-compose.yml')
$Exe = if ($env:OS -eq 'Windows_NT') { '.exe' } else { '' }

# generators installed by `tools` live in GOPATH/bin
$gobin = Join-Path (& go env GOPATH) 'bin'
$env:PATH = "$env:PATH$([IO.Path]::PathSeparator)$gobin"

# Run a native command and stop on a non-zero exit code.
function Run {
    $cmd, $rest = $args
    & $cmd @rest
    if ($LASTEXITCODE -ne 0) { throw "$cmd $($rest -join ' ') failed (exit $LASTEXITCODE)" }
}

function Tools {
    Run go install github.com/bufbuild/buf/cmd/buf@latest
    Run go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    Run go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
}

function Generate {
    Run buf lint
    Run buf generate
}

function Build {
    New-Item -ItemType Directory -Force bin | Out-Null
    foreach ($s in $Services) { Run go build -o "bin/$s$Exe" "./cmd/$s" }
}

function Test { Run go test ./... }

function TestPg {
    $old = $env:GOAP_TEST_PG_DSN
    if (-not $old) { $env:GOAP_TEST_PG_DSN = 'postgres://goap:goap@localhost:5432/goap?sslmode=disable' }
    try { Run go test ./pkg/graph/... ./internal/... }
    finally { $env:GOAP_TEST_PG_DSN = $old }
}

function Lint {
    Run go vet ./...
    $unformatted = & gofmt -l . | Where-Object { $_ -and $_ -notmatch 'node_modules' }
    if ($unformatted) {
        $unformatted | ForEach-Object { Write-Host $_ }
        throw 'gofmt: files above are not formatted'
    }
    $a = (Get-FileHash docs/dsl.md).Hash
    $b = (Get-FileHash web/src/lib/help/dsl.md).Hash
    if ($a -ne $b) { throw 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' }
}

# the IDE is rebuilt when its sources change (served by goap-dev)
function WebDist {
    $dist = 'web/dist/index.html'
    $sources = @('web/package.json', 'web/index.html', 'web/vite.config.ts') | Get-Item
    $sources += Get-ChildItem web/src -Recurse -File -ErrorAction SilentlyContinue
    $newest = ($sources | Measure-Object LastWriteTime -Maximum).Maximum
    if ((Test-Path $dist) -and ((Get-Item $dist).LastWriteTime -ge $newest)) { return }
    Push-Location web
    try {
        Run npm install --no-audit --no-fund
        Run npm run build
    }
    finally { Pop-Location }
}

function Dev { Run go run ./cmd/goap-dev }

function DevLocal {
    WebDist
    $old = $env:GOAP_STORE
    $env:GOAP_STORE = 'sqlite'
    try { Run go run ./cmd/goap-dev }
    finally { $env:GOAP_STORE = $old }
}

function DevLocalReset { Remove-Item -Recurse -Force .goap -ErrorAction SilentlyContinue }

function Web {
    Push-Location web
    try {
        Run npm install
        Run npm run dev
    }
    finally { Pop-Location }
}

function RunnerImage { Run docker build --build-arg SERVICE=goap-runner -t goap/runner:dev . }

function Up {
    RunnerImage
    Run docker @Compose up --build -d
}

function Down { Run docker @Compose down }
function Logs { Run docker @Compose logs -f engine graph registry modelgw gateway }

function Help {
    @'
Usage: .\make.ps1 <target>

  all             generate, build, test
  tools           install code generators
  generate        regenerate connect-rpc code from proto/
  build           build all services into bin\
  test            go test ./...
  test-pg         tests against PostgreSQL (graph, registry, iam)
  lint            go vet + gofmt + dsl.md sync check
  dev             single process, in-memory, demo data: http://localhost:8080
  devlocal        no docker: SQLite (.goap\goap.db) + IDE on http://localhost:8080
  devlocal-reset  drop the local SQLite database
  web             IDE dev server (vite)
  runner-image    image of the script sandboxes (goap-runner)
  up | down | logs   docker compose stack
'@ | Write-Host
}

switch ($Target) {
    'all'            { Generate; Build; Test }
    'tools'          { Tools }
    'generate'       { Generate }
    'build'          { Build }
    'test'           { Test }
    'test-pg'        { TestPg }
    'lint'           { Lint }
    'dev'            { Dev }
    'devlocal'       { DevLocal }
    'devlocal-reset' { DevLocalReset }
    'web'            { Web }
    'runner-image'   { RunnerImage }
    'up'             { Up }
    'down'           { Down }
    'logs'           { Logs }
    { $_ -in 'help', '-h', '--help' } { Help }
    default          { Help; throw "unknown target: $Target" }
}
