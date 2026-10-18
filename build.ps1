# Builds Movie night for Windows (installer) and Ubuntu (.deb) into dist\.
#   powershell -ExecutionPolicy Bypass -File build.ps1 [-Version 1.0.0]
param([string]$Version = "1.0.0")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
$gobin = Join-Path (go env GOPATH) "bin"

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Check { if ($LASTEXITCODE -ne 0) { throw "step failed (exit $LASTEXITCODE)" } }

# cloudflared (Apache-2.0) gives the invite link its public address. Pinned, with
# the SHA256 sums published in the release notes on github.com/cloudflare/cloudflared.
$CloudflaredVersion = "2026.9.3"
$CloudflaredSHA256 = @{
    "cloudflared-windows-amd64.exe" = "f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2"
    "cloudflared-linux-amd64"       = "77e26d8d900e0b8469f416239d14b5f296525fdf79fee6f511ef55609e3fbac2"
}
function Get-Cloudflared($name) {
    $dir = "build\cache\cloudflared-$CloudflaredVersion"
    $path = Join-Path $dir $name
    if (-not (Test-Path $path)) {
        New-Item -ItemType Directory -Force $dir | Out-Null
        Step "Downloading $name $CloudflaredVersion"
        Invoke-WebRequest -UseBasicParsing -OutFile "$path.part" "https://github.com/cloudflare/cloudflared/releases/download/$CloudflaredVersion/$name"
        Move-Item -Force "$path.part" $path
    }
    $hash = (Get-FileHash -Algorithm SHA256 $path).Hash.ToLower()
    if ($hash -ne $CloudflaredSHA256[$name]) {
        Remove-Item $path
        throw "$name checksum mismatch (got $hash)"
    }
    return $path
}

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

Step "Bundling ffmpeg and cloudflared"
$ff = (Get-Command ffmpeg -ErrorAction Stop).Source
$target = (Get-Item $ff).Target
if ($target) { $ff = $target | Select-Object -First 1 }
Copy-Item $ff dist\windows\ffmpeg.exe
Copy-Item (Get-Cloudflared "cloudflared-windows-amd64.exe") dist\windows\cloudflared.exe

Step "Building Windows installer"
$iscc = @("$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe") |
    Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) { throw "Inno Setup not found: winget install JRSoftware.InnoSetup" }
& $iscc /Q "/DAppVersion=$Version" packaging\windows\movienight.iss; Check

Step "Building Linux app"
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o dist\linux\movienight .; Check
Copy-Item (Get-Cloudflared "cloudflared-linux-amd64") dist\linux\cloudflared
Remove-Item Env:GOOS, Env:GOARCH

Step "Building Ubuntu .deb"
$env:VERSION = $Version
& "$gobin\nfpm.exe" package --config packaging\linux\nfpm.yaml --packager deb --target dist\; Check

Step "Done"
Get-ChildItem dist -File | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
