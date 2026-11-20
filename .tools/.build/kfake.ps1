#requires -Version 5.1
<#
.SYNOPSIS
 검증용 인메모리 Kafka (franz-go kfake). .tools\.build 포터블 Go 로 빌드한 뒤 127.0.0.1:19092 에 띄운다.
.EXAMPLE
 .\.tools\.build\kfake.ps1
 다른 창: .\kv-console-windows-amd64.exe -brokers 127.0.0.1:19092 -listen 127.0.0.1:8089
 한 번에: .\demo.ps1
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$activate = Join-Path $PSScriptRoot 'activate.ps1'
if (-not (Test-Path $activate)) { throw ".tools\.build\activate.ps1 이 없습니다. 먼저 .\.tools\.build\go.ps1 -Action install" }
. $activate
$dir = Join-Path $PSScriptRoot 'kfake'
Push-Location $dir
try {
	& go build -o kfake.exe .
	if ($LASTEXITCODE) { throw "kfake 빌드 실패 (exit $LASTEXITCODE)" }
} finally { Pop-Location }
& (Join-Path $dir 'kfake.exe')
