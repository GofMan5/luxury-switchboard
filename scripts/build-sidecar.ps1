$ErrorActionPreference = "Stop"

$workspace = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $workspace "backend"
$binaryDirectory = Join-Path $workspace "src-tauri\binaries"
$hostLine = rustc -vV | Select-String "^host: "
if (-not $hostLine) {
    throw "Unable to determine Rust host target"
}
$target = $hostLine.Line.Substring(6).Trim()
$extension = if ($target -like "*-windows-*") { ".exe" } else { "" }
$output = Join-Path $binaryDirectory "switchboard-sidecar-$target$extension"

New-Item -ItemType Directory -Path $binaryDirectory -Force | Out-Null
Push-Location $backend
try {
    go build -trimpath -o $output ./cmd/switchboard-sidecar
    if ($LASTEXITCODE -ne 0) {
        throw "Go sidecar build failed"
    }
}
finally {
    Pop-Location
}

Write-Output "Built $output"
