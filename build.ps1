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
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "pnpm install 失败 (exit $LASTEXITCODE)" }
pnpm run typecheck
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "pnpm typecheck 失败 (exit $LASTEXITCODE)" }
pnpm run build
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "pnpm build 失败 (exit $LASTEXITCODE)" }
Pop-Location

Write-Host '验证 Go 工程'
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test 失败 (exit $LASTEXITCODE)" }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw "go vet 失败 (exit $LASTEXITCODE)" }

$Targets = @(
    @{ OS = 'darwin'; Arch = 'amd64' },
    @{ OS = 'darwin'; Arch = 'arm64' },
    @{ OS = 'linux'; Arch = 'amd64' },
    @{ OS = 'linux'; Arch = 'arm64' },
    @{ OS = 'windows'; Arch = 'amd64' },
    @{ OS = 'windows'; Arch = 'arm64' }
)

$HostOs = (& go env GOHOSTOS)
$HostArch = (& go env GOHOSTARCH)
# 没装 C 编译器时，宿主平台也按 CGO_ENABLED=0 构建，否则 cgo 目标会直接失败
$HasCC = [bool](Get-Command gcc -ErrorAction SilentlyContinue)
if (-not $HasCC) {
    Write-Host '未检测到 gcc，宿主平台同样使用 CGO_ENABLED=0'
}

foreach ($Target in $Targets) {
    $Name = "$AppName-$($Target.OS)-$($Target.Arch)"
    if ($Target.OS -eq 'windows') { $Name += '.exe' }
    Write-Host "构建 $($Target.OS)/$($Target.Arch) -> $Name"

    # 与 build.sh 策略一致：目标平台 == 当前机器时允许 CGO
    $Cgo = '0'
    if ($Target.OS -eq $HostOs -and $Target.Arch -eq $HostArch -and $HasCC) { $Cgo = '1' }

    $env:GOOS = $Target.OS
    $env:GOARCH = $Target.Arch
    $env:CGO_ENABLED = $Cgo
    go build -trimpath '-ldflags=-s -w' -o (Join-Path $DistDir $Name) .
    # PowerShell 5.1 下原生命令失败不会触发 $ErrorActionPreference，必须显式检查
    if ($LASTEXITCODE -ne 0) { throw "构建失败: $($Target.OS)/$($Target.Arch) (exit $LASTEXITCODE)" }
}

$env:GOOS = $null
$env:GOARCH = $null
$env:CGO_ENABLED = $null

Copy-Item (Join-Path $RootDir 'README.md') $DistDir
Copy-Item (Join-Path $RootDir 'build.sh'), (Join-Path $RootDir 'build.ps1'), (Join-Path $RootDir 'build.cmd') $DistDir

$DocsDir = Join-Path $DistDir 'docs'
New-Item -ItemType Directory -Force -Path $DocsDir | Out-Null
Copy-Item (Join-Path $RootDir 'docs/*.md') $DocsDir

$HandleDir = Join-Path $RootDir 'Handle'
if (Test-Path $HandleDir) {
    Copy-Item $HandleDir (Join-Path $DistDir 'Handle') -Recurse -Force
}

$Expected = @(
    "$AppName-darwin-amd64",
    "$AppName-darwin-arm64",
    "$AppName-linux-amd64",
    "$AppName-linux-arm64",
    "$AppName-windows-amd64.exe",
    "$AppName-windows-arm64.exe"
)
$Missing = $Expected | Where-Object { -not (Test-Path (Join-Path $DistDir $_)) }
if ($Missing) { throw "构建产物缺失: $($Missing -join ', ')" }

$Artifacts = Get-ChildItem $DistDir -File | Where-Object { $_.Name -like "$AppName-*" }
$Hashes = foreach ($Artifact in $Artifacts | Sort-Object Name) {
    $Hash = (Get-FileHash $Artifact.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$Hash  $($Artifact.Name)"
}
$Hashes | Set-Content (Join-Path $DistDir 'SHA256SUMS') -Encoding ascii
Write-Host "完成，输出目录: $DistDir"
