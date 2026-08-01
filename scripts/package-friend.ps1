param(
    [string]$InstallerPath,
    [string]$OutputPath,
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$workspace = Split-Path -Parent $PSScriptRoot
if (-not $InstallerPath) {
    $version = (Get-Content -Raw -LiteralPath (Join-Path $workspace "src-tauri\tauri.conf.json") | ConvertFrom-Json).version
    $InstallerPath = Join-Path $workspace "src-tauri\target\release\bundle\nsis\Switchboard_${version}_x64-setup.exe"
}
$installer = (Resolve-Path -LiteralPath $InstallerPath).Path
$checksumPath = Join-Path $workspace "SHA256SUMS.txt"
$checksumLines = @(Get-Content -LiteralPath $checksumPath | Where-Object { $_ -match '^[0-9A-Fa-f]{64}\s{2}.+$' })
if ($checksumLines.Count -ne 1 -or $checksumLines[0] -notmatch '^([0-9A-Fa-f]{64})\s{2}(.+)$') {
    throw "Release checksum is pending or malformed"
}
$expectedHash = $Matches[1].ToUpperInvariant()
$expectedName = $Matches[2]
if ($expectedName -ne (Split-Path -Leaf $installer)) {
    throw "Release checksum names a different installer"
}
$actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $installer).Hash
if ($actualHash -ne $expectedHash) {
    throw "Installer does not match SHA256SUMS.txt"
}

$releaseCommit = (git -C $workspace rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $releaseCommit -notmatch '^[0-9a-f]{40}$') {
    throw "Git source commit is unavailable"
}
$dirty = @(git -C $workspace status --porcelain --untracked-files=no)
if ($LASTEXITCODE -ne 0 -or $dirty.Count -ne 0) {
    throw "Tracked source changes are not committed"
}
$checksumCommit = (git -C $workspace log -1 --format=%H -- SHA256SUMS.txt).Trim()
if ($LASTEXITCODE -ne 0 -or $checksumCommit -ne $releaseCommit) {
    throw "Source changed after the release checksum was recorded"
}
$sourceCommit = (git -C $workspace rev-parse "$releaseCommit^").Trim()
$releaseFiles = @(git -C $workspace diff --name-only $sourceCommit $releaseCommit)
if ($LASTEXITCODE -ne 0 -or $sourceCommit -notmatch '^[0-9a-f]{40}$' -or $releaseFiles.Count -ne 1 -or $releaseFiles[0] -ne "SHA256SUMS.txt") {
    throw "Release checksum commit contains source changes"
}
if (-not $OutputPath) {
    $OutputPath = Join-Path $workspace ("artifacts\Switchboard-friend-{0}.zip" -f $releaseCommit.Substring(0, 8))
}
$output = [IO.Path]::GetFullPath($OutputPath)
$outputDirectory = Split-Path -Parent $output
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
if (Test-Path -LiteralPath $output) {
    if (-not $Force) {
        throw "Output archive already exists; pass -Force to replace it"
    }
    Remove-Item -LiteralPath $output -Force
}

$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$staging = Join-Path $tempRoot ("switchboard-package-" + [guid]::NewGuid().ToString("N"))
$package = Join-Path $staging "Switchboard"
New-Item -ItemType Directory -Force -Path $package | Out-Null
try {
    Copy-Item -LiteralPath $installer -Destination (Join-Path $package (Split-Path -Leaf $installer))
    Copy-Item -LiteralPath $checksumPath -Destination (Join-Path $package "SHA256SUMS.txt")
    Copy-Item -LiteralPath (Join-Path $workspace "README.md") -Destination (Join-Path $package "README.md")
    Copy-Item -LiteralPath (Join-Path $workspace "FRIEND-SETUP.md") -Destination (Join-Path $package "FRIEND-SETUP.md")
    @("source_commit=$sourceCommit", "release_commit=$releaseCommit", "installer_sha256=$actualHash") | Set-Content -LiteralPath (Join-Path $package "BUILD.txt") -Encoding utf8
    Compress-Archive -LiteralPath $package -DestinationPath $output -CompressionLevel Optimal

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($output)
    try {
        $forbidden = $archive.Entries | Where-Object { $_.FullName -match '(?i)(id_(rsa|ed25519)|model-tunnel.*ed25519|\.pem$|\.key$|\.p12$|\.pfx$|\.dpapi$|\.db($|-))' }
        if ($forbidden) {
            throw "Archive contains forbidden credential or state files"
        }
    }
    finally {
        $archive.Dispose()
    }
    $zipHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $output).Hash
    Write-Output "Created $output"
    Write-Output "SHA256 $zipHash"
}
catch {
    if (Test-Path -LiteralPath $output) {
        Remove-Item -LiteralPath $output -Force
    }
    throw
}
finally {
    $resolvedStaging = [IO.Path]::GetFullPath($staging)
    if ($resolvedStaging.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $resolvedStaging)) {
        Remove-Item -LiteralPath $resolvedStaging -Recurse -Force
    }
}
