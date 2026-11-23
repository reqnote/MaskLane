[CmdletBinding()]
param([switch]$Test)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$work = Join-Path $root '.build'
$dist = Join-Path $root 'dist'
$sourceRevision = '556048c1c18d84f7d61d28f686220178a4ff2c4d'
$source = Join-Path $work "pipelock-$sourceRevision"
$go = Join-Path $work 'go/bin/go.exe'
$patch = Join-Path $root 'patches/pipelock-3.8.0.patch'
$overlay = Join-Path $root 'overlay'

function Get-VerifiedArchive($Uri, $Archive, $Hash) {
    if (-not (Test-Path -LiteralPath $Archive)) {
        $partial = "$Archive.part"
        & curl.exe --fail --silent --show-error --location --max-time 180 --output $partial $Uri
        if ($LASTEXITCODE -ne 0) { throw "Download failed ($LASTEXITCODE): $Uri" }
        if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $Hash) {
            throw "Archive checksum mismatch: $Uri"
        }
        Move-Item -LiteralPath $partial -Destination $Archive
    }
    if ((Get-FileHash -LiteralPath $Archive -Algorithm SHA256).Hash -ne $Hash) {
        throw "Archive checksum mismatch: $Archive"
    }
}

function Expand-VerifiedArchive($Archive) {
    & tar.exe -xf $Archive -C $work
    if ($LASTEXITCODE -ne 0) { throw "Archive extraction failed: $Archive" }
}

New-Item -ItemType Directory -Path $work, $dist -Force | Out-Null
if (-not (Test-Path -LiteralPath (Join-Path $source 'go.mod'))) {
    $sourceZip = Join-Path $work 'pipelock-556048c1.zip'
    Get-VerifiedArchive "https://codeload.github.com/pipelab-org/pipelock/zip/$sourceRevision" $sourceZip `
        '7602c5a665b9681f624e8dd96b32af2604e30f1e63b637ec29ba2b2baa77110d'
    Expand-VerifiedArchive $sourceZip
}
if (-not (Test-Path -LiteralPath $go)) {
    $goZip = Join-Path $work 'go1.26.10.windows-amd64.zip'
    Get-VerifiedArchive 'https://dl.google.com/go/go1.26.10.windows-amd64.zip' $goZip `
        '7ecd2d7260e82c59bcbd7f1077a07cee2dea607295b858df67195a1e4e09629b'
    Expand-VerifiedArchive $goZip
}

& git -C $source apply --reverse --check $patch 2>$null
if ($LASTEXITCODE -ne 0) {
    & git -C $source apply --check $patch
    if ($LASTEXITCODE -ne 0) { throw 'The pinned Pipelock source does not match the patch.' }
    & git -C $source apply $patch
    if ($LASTEXITCODE -ne 0) { throw 'Failed to apply the Pipelock patch.' }
}

Get-ChildItem -LiteralPath $overlay -Recurse -File | ForEach-Object {
    $relative = $_.FullName.Substring($overlay.Length + 1)
    $target = Join-Path $source $relative
    New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
    Copy-Item -LiteralPath $_.FullName -Destination $target -Force
}

$cacheSettings = @{
    GOCACHE = Join-Path $work 'go-cache'
    GOMODCACHE = Join-Path $work 'go-mod'
    GOTOOLCHAIN = 'local'
    CGO_ENABLED = '0'
    GOOS = 'windows'
    GOARCH = 'amd64'
}
$previous = @{}
foreach ($name in $cacheSettings.Keys) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    [Environment]::SetEnvironmentVariable($name, $cacheSettings[$name], 'Process')
}

Push-Location -LiteralPath $source
try {
    if ($Test) {
        & $go test ./cmd/masking-gateway ./internal/redact
        if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
        & $go test ./internal/proxy -run '^TestMaskingGateway(TLS|Intercept)$'
        if ($LASTEXITCODE -ne 0) { throw 'Proxy tests failed.' }
    }
    $exe = Join-Path $dist 'MaskingGateway.exe'
    & $go build -trimpath -buildvcs=false -ldflags '-s -w' -o $exe ./cmd/masking-gateway
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
} finally {
    Pop-Location
    foreach ($name in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}

Copy-Item -LiteralPath (Join-Path $source 'LICENSE') -Destination (Join-Path $dist 'PIPELOCK-LICENSE.txt') -Force
Copy-Item -LiteralPath (Join-Path $source 'NOTICE') -Destination (Join-Path $dist 'PIPELOCK-NOTICE.txt') -Force
Get-ChildItem -LiteralPath (Join-Path $root 'pilot') -File | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $dist -Force
}
$contents = 'MaskingGateway.exe', 'PIPELOCK-LICENSE.txt', 'PIPELOCK-NOTICE.txt' | ForEach-Object {
    Join-Path $dist $_
}
$contents += Get-ChildItem -LiteralPath (Join-Path $root 'pilot') -File | ForEach-Object {
    Join-Path $dist $_.Name
}
$zip = Join-Path $dist 'MaskingGateway-Windows-x64.zip'
Compress-Archive -LiteralPath $contents -DestinationPath $zip -Force
Write-Host $exe
Write-Host $zip
