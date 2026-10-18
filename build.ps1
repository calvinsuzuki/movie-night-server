# Builds Movie night for Windows (installer) and Ubuntu (.deb) into dist\.
#   powershell -ExecutionPolicy Bypass -File build.ps1 [-Version 1.0.0]
param([string]$Version = "1.0.0")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
$gobin = Join-Path (go env GOPATH) "bin"

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Check { if ($LASTEXITCODE -ne 0) { throw "step failed (exit $LASTEXITCODE)" } }

if (-not (Test-Path assets\icon.ico)) { Step "Drawing icons"; go run ./tools/genicon; Check }

Remove-Item -Recurse -Force dist\windows, dist\linux -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force dist\windows, dist\linux | Out-Null

Step "Embedding Windows icon and version info"
& "$gobin\go-winres.exe" simply --arch amd64 --manifest gui --icon assets\icon.ico `
    --product-name "Movie night" --file-description "Movie night" --original-filename movienight.exe `
    --copyright "calvinsuzuki" `
    --product-version $Version --file-version $Version --out rsrc; Check

Step "Building Windows app"
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\windows\movienight.exe .; Check

Step "Bundling ffmpeg"
$ff = (Get-Command ffmpeg -ErrorAction Stop).Source
$target = (Get-Item $ff).Target
if ($target) { $ff = $target | Select-Object -First 1 }
Copy-Item $ff dist\windows\ffmpeg.exe

Step "Building Windows installer"
$iscc = @("$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe") |
    Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) { throw "Inno Setup not found: winget install JRSoftware.InnoSetup" }
& $iscc /Q "/DAppVersion=$Version" packaging\windows\movienight.iss; Check

Step "Building Linux app"
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o dist\linux\movienight .; Check
Remove-Item Env:GOOS, Env:GOARCH

Step "Building Ubuntu .deb"
$env:VERSION = $Version
& "$gobin\nfpm.exe" package --config packaging\linux\nfpm.yaml --packager deb --target dist\; Check

Step "Done"
Get-ChildItem dist -File | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
