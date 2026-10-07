<#
.SYNOPSIS
  Windows equivalent of the Makefile (PowerShell 5.1+ ships with Windows, no `make` needed): the same targets, doing the
  same things. Keep the two in step.

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

$Services = 'graph', 'registry', 'engine', 'modelgw', 'preferences', 'conversations', 'credentials', 'indexer', 'events', 'gateway', 'mcp', 'connector-localfs', 'goap-dev', 'goap-runner'
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

# Run a block in a directory, coming back whatever happens.
function InDir($dir, [scriptblock]$block) {
    Push-Location $dir
    try { & $block }
    finally { Pop-Location }
}

function Tools {
    Run go install github.com/bufbuild/buf/cmd/buf@latest
    Run go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    Run go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
    Run go install github.com/air-verse/air@latest
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

function WebTest { InDir web { Run npm test } }

# every test, the PostgreSQL ones included: GOAP_TEST_PG_DSN, else a throw-away pgvector container on PGTEST_PORT
function TestPg {
    if ($env:GOAP_TEST_PG_DSN) { Run go test -count=1 ./...; return }
    $image = if ($env:PGTEST_IMAGE) { $env:PGTEST_IMAGE } else { 'pgvector/pgvector:pg17' }
    $port = if ($env:PGTEST_PORT) { $env:PGTEST_PORT } else { '55432' }
    $c = & docker run -d --rm -e POSTGRES_USER=goap -e POSTGRES_PASSWORD=goap -e POSTGRES_DB=goap -p "${port}:5432" $image
    if ($LASTEXITCODE -ne 0) { throw "docker run $image failed (exit $LASTEXITCODE)" }
    try {
        # the server starts twice (init, then for real): wait for the second "ready"; docker logs writes to stderr
        while (@(& { $ErrorActionPreference = 'Continue'; & docker logs $c 2>&1 } | Select-String 'ready to accept connections').Count -lt 2) {
            Start-Sleep -Seconds 1
        }
        $env:GOAP_TEST_PG_DSN = "postgres://goap:goap@localhost:$port/goap?sslmode=disable"
        try { Run go test -count=1 ./... }
        finally { Remove-Item Env:GOAP_TEST_PG_DSN -ErrorAction SilentlyContinue }
    }
    finally { & docker stop $c | Out-Null }
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

function Dev { Run go run ./cmd/goap-dev }

# SQLite backend only, rebuilt and restarted by air on .go changes: http://localhost:8080. .air.toml is written for a
# POSIX shell (an environment prefix, a binary with no extension): its build settings are given on the command line here.
function DevLocalBackend {
    if (-not (Get-Command air -ErrorAction SilentlyContinue)) { Run go install github.com/air-verse/air@latest }
    $bin = ".air/goap-dev$Exe"
    $old = $env:GOAP_STORE
    $env:GOAP_STORE = 'sqlite'
    try { Run air -c .air.toml --build.cmd "go build -o $bin ./cmd/goap-dev" --build.entrypoint $bin --build.full_bin $bin }
    finally { $env:GOAP_STORE = $old }
}

# IDE only, hot-reloaded by vite, proxied to the backend: http://localhost:5173
function DevLocalWeb {
    InDir web {
        Run npm install --no-audit --no-fund
        Run npm run dev
    }
}

# on this machine, no docker: SQLite (.goap/goap.db), backend + IDE both reload on code change: http://localhost:5173
function DevLocal {
    $shell = (Get-Process -Id $PID).Path # powershell.exe or pwsh, whichever runs this script
    $backend = Start-Process -FilePath $shell -NoNewWindow -PassThru -ArgumentList @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $PSScriptRoot 'make.ps1'), 'devlocal-backend')
    try { DevLocalWeb }
    finally {
        # the backend and what it started (air, goap-dev)
        if ($env:OS -eq 'Windows_NT') { & taskkill /T /F /PID $backend.Id 2>&1 | Out-Null }
        else { Stop-Process -Id $backend.Id -Force -ErrorAction SilentlyContinue }
    }
}

function DevLocalReset { Remove-Item -Recurse -Force .goap -ErrorAction SilentlyContinue }

function RunnerImage { Run docker build --build-arg SERVICE=goap-runner -t goap/runner:dev . }

# Engine socket for the docker-proxy service: Docker Desktop keeps it in its VM, at the default path
function DockerSock { if (-not $env:GOAP_DOCKER_SOCK) { $env:GOAP_DOCKER_SOCK = '/var/run/docker.sock' } }

function Up {
    RunnerImage
    DockerSock
    Run docker @Compose up --build -d
}

function Down { DockerSock; Run docker @Compose down }
function Logs { DockerSock; Run docker @Compose logs -f engine graph registry modelgw gateway }

function Help {
    @'
Usage: .\make.ps1 <target>   (or .\make.cmd <target>)

  all               generate, build, test
  tools             install code generators (and air)
  generate          regenerate connect-rpc code from proto/
  build             build all services into bin\
  test              go test ./...
  web-test          unit tests of the web (needs npm install in web\)
  test-pg           every test, the PostgreSQL ones included (GOAP_TEST_PG_DSN, else a throw-away
                    pgvector container on PGTEST_PORT, 55432)
  lint              go vet + gofmt + dsl.md sync check
  dev               single process, in-memory, demo data: http://localhost:8080
  devlocal          no docker: SQLite (.goap\goap.db), backend + IDE both reload on code change:
                    http://localhost:5173
  devlocal-backend  SQLite backend only, rebuilt and restarted by air: http://localhost:8080
  devlocal-web      IDE only, hot-reloaded by vite, proxied to the backend: http://localhost:5173
  devlocal-reset    drop the local SQLite database
  web               same as devlocal-web
  runner-image      image of the script sandboxes (goap-runner)
  up | down | logs  docker compose stack
'@ | Write-Host
}

switch ($Target) {
    'all'              { Generate; Build; Test }
    'tools'            { Tools }
    'generate'         { Generate }
    'build'            { Build }
    'test'             { Test }
    'web-test'         { WebTest }
    'test-pg'          { TestPg }
    'lint'             { Lint }
    'dev'              { Dev }
    'devlocal'         { DevLocal }
    'devlocal-backend' { DevLocalBackend }
    'devlocal-web'     { DevLocalWeb }
    'devlocal-reset'   { DevLocalReset }
    'web'              { DevLocalWeb }
    'runner-image'     { RunnerImage }
    'up'               { Up }
    'down'             { Down }
    'logs'             { Logs }
    { $_ -in 'help', '-h', '--help' } { Help }
    default            { Help; throw "unknown target: $Target" }
}
