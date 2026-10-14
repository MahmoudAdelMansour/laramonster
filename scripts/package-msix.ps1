# Windows only: packs the Windows build into dist/LaraMonster-windows-x64.msix
# for the Microsoft Store (unsigned: the Store signs it on publish).
#   pwsh scripts/package-msix.ps1 -Tag v0.2.0 -App dist/LaraMonster-windows-amd64.exe -Cli dist/laramonster-cli-windows-amd64.exe
param(
  [Parameter(Mandatory)] [string] $Tag,
  [Parameter(Mandatory)] [string] $App,
  [Parameter(Mandatory)] [string] $Cli,
  [string] $Out = "dist/LaraMonster-windows-x64.msix"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

# Store versions are four numbers and the last must be 0: v0.2.0-beta.1 → 0.2.0.0
$core = ($Tag -replace '^v', '') -replace '[-+].*$', ''
# Manual test runs aren't tagged ("main"): any valid version will do.
if ($core -notmatch '^\d+(\.\d+){0,2}$') { $core = '0.0.1' }
$parts = @($core.Split('.') + @('0', '0', '0'))[0..2]
$version = ($parts -join '.') + '.0'

$stage = Join-Path ([IO.Path]::GetTempPath()) ("msix-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $stage | Out-Null
# Different names on purpose: Windows file names ignore case.
Copy-Item $App (Join-Path $stage "LaraMonsterApp.exe")
Copy-Item $Cli (Join-Path $stage "laramonster.exe")
Copy-Item -Recurse (Join-Path $root "build/windows/msix/Assets") (Join-Path $stage "Assets")
(Get-Content (Join-Path $root "build/windows/msix/AppxManifest.xml") -Raw).Replace("__VERSION__", $version) |
  Set-Content -Encoding utf8 (Join-Path $stage "AppxManifest.xml")

$makeappx = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\makeappx.exe" |
  Sort-Object FullName -Descending | Select-Object -First 1
if (-not $makeappx) { throw "makeappx.exe not found (Windows SDK)" }
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null
& $makeappx.FullName pack /o /d $stage /p $Out
if ($LASTEXITCODE -ne 0) { throw "makeappx failed" }
Write-Host "$Out (version $version)"
