$ErrorActionPreference = 'Stop'

$RootDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DistDir = Join-Path $RootDir 'dist'
$AppName = 'golangtools'

Set-Location $RootDir
if (Test-Path $DistDir) {
    Remove-Item -Recurse -Force $DistDir
}
New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

Write-Host '构建 Vue 前端'
Push-Location (Join-Path $RootDir 'web')
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
Pop-Location

Write-Host '验证 Go 工程'
go test ./...
go vet ./...

$Targets = @(
    @{ OS = 'darwin'; Arch = 'amd64' },
    @{ OS = 'darwin'; Arch = 'arm64' },
    @{ OS = 'linux'; Arch = 'amd64' },
    @{ OS = 'linux'; Arch = 'arm64' },
    @{ OS = 'windows'; Arch = 'amd64' },
    @{ OS = 'windows'; Arch = 'arm64' }
)

foreach ($Target in $Targets) {
    $Name = "$AppName-$($Target.OS)-$($Target.Arch)"
    if ($Target.OS -eq 'windows') { $Name += '.exe' }
    Write-Host "构建 $($Target.OS)/$($Target.Arch) -> $Name"
    $env:GOOS = $Target.OS
    $env:GOARCH = $Target.Arch
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags='-s -w' -o (Join-Path $DistDir $Name) .
}

$env:GOOS = $null
$env:GOARCH = $null
$env:CGO_ENABLED = $null

Copy-Item (Join-Path $RootDir 'README.md') $DistDir
$DocsDir = Join-Path $DistDir 'docs'
New-Item -ItemType Directory -Force -Path $DocsDir | Out-Null
Copy-Item (Join-Path $RootDir 'docs/*.md') $DocsDir

$Artifacts = Get-ChildItem $DistDir -File | Where-Object { $_.Name -like "$AppName-*" }
$Hashes = foreach ($Artifact in $Artifacts | Sort-Object Name) {
    $Hash = (Get-FileHash $Artifact.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$Hash  $($Artifact.Name)"
}
$Hashes | Set-Content (Join-Path $DistDir 'SHA256SUMS') -Encoding ascii
Write-Host "完成，输出目录: $DistDir"
