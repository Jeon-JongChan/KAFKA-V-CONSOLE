#requires -Version 5.1
<#
.SYNOPSIS
 Kafka V-Console 포터블 빌드. .build 킷으로 Go 를 프로젝트 로컬에 준비하고 단일 바이너리를 만든다.
.DESCRIPTION
 1) .build\go.ps1 install  : Go SDK 를 .build\go 에 설치 (이미 있으면 건너뜀)
 2) .build\go.ps1 prime    : 모듈 다운로드 + go test  (-Offline 이면 offline-check: 캐시만 사용)
 3) 대상 OS/아키텍처별 CGO 없는 정적 바이너리를 bin\ 에 생성 (프론트는 web\ 이 embed 로 포함됨)
.EXAMPLE
 .\build.ps1                      # 윈도우 + 리눅스(amd64/arm64)
 .\build.ps1 -Targets linux/amd64 # 리눅스만
 .\build.ps1 -Offline             # 인터넷 없이, 캐시된 모듈로만
#>
[CmdletBinding()]
param(
 [string[]]$Targets=@('windows/amd64','linux/amd64','linux/arm64'),
 [switch]$Offline
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$kit=Join-Path $PSScriptRoot '.build\go.ps1'

# 오프라인이면 설치 단계를 건너뛰고 이미 준비된 SDK/모듈 캐시만 검증한다
if (-not $Offline) { & $kit -Action install }
& $kit -Action $(if ($Offline) { 'offline-check' } else { 'prime' })
. (Join-Path $PSScriptRoot '.build\activate.ps1')
$node=Join-Path $PSScriptRoot '.build\nodejs\node\node.exe'
if (Test-Path $node) { & $node (Join-Path $PSScriptRoot 'graph_check.mjs'); if ($LASTEXITCODE) { throw 'graph_check 실패' } }

$version=Get-Date -Format 'yyyy.MM.dd-HHmm'
$out=Join-Path $PSScriptRoot 'bin'
New-Item -ItemType Directory -Path $out -Force | Out-Null
$env:CGO_ENABLED='0'
try {
 foreach ($t in $Targets) {
  $env:GOOS,$env:GOARCH=$t.Split('/')
  $exe=Join-Path $out ("kv-console-$($env:GOOS)-$($env:GOARCH)" + $(if ($env:GOOS -eq 'windows') { '.exe' } else { '' }))
  Push-Location $PSScriptRoot
  try { & go build -trimpath -ldflags "-s -w -X main.version=$version" -o $exe . ; if ($LASTEXITCODE) { throw "go build 실패: $t" } }
  finally { Pop-Location }
  Write-Host ("[build] {0}  ({1:N1} MB)" -f $exe, ((Get-Item $exe).Length / 1MB))
 }
} finally { Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED -ErrorAction SilentlyContinue }
# 리눅스 실행 스크립트: CRLF 면 sh 가 깨지므로 LF + BOM 없는 UTF-8 로 강제 복사
$sh=(Get-Content -Raw -Encoding UTF8 (Join-Path $PSScriptRoot 'kv-console.sh')) -replace "`r`n","`n"
[IO.File]::WriteAllText((Join-Path $out 'kv-console.sh'), $sh, (New-Object Text.UTF8Encoding $false))
Write-Host "[build] $(Join-Path $out 'kv-console.sh')"
