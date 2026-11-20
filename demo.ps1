#requires -Version 5.1
<#
.SYNOPSIS
 가짜 Kafka(kfake) + kv-console 로컬 데모/스모크 테스트.
.DESCRIPTION
 1) 포터블 Go 준비
 2) kfake 를 127.0.0.1:19092 에 기동
 3) kv-console 빌드 후 연결
 4) /api/cluster 응답 확인 (선택: 브라우저 오픈)
 Smart App Control 이 미서명 Windows exe 를 막으면(오류 4551) 자동으로 WSL 리눅스 바이너리로 폴백한다.
 종료: Ctrl+C — kfake/콘솔 프로세스도 같이 정리한다.
.EXAMPLE
  .\demo.ps1
  .\demo.ps1 -NoBrowser
  .\demo.ps1 -SmokeOnly
  .\demo.ps1 -Wsl          # Windows exe 시도 없이 WSL 만 사용
#>
[CmdletBinding()]
param(
  [switch]$NoBrowser,
  [switch]$SmokeOnly,
  [switch]$Wsl
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$kit = Join-Path $root '.tools\.build\go.ps1'
$kfakeDir = Join-Path $root '.tools\.build\kfake'
$brokers = '127.0.0.1:19092'
$listen = '127.0.0.1:8089'
$winExe = Join-Path $root 'kv-console-windows-amd64.exe'
$linuxExe = Join-Path $root 'kv-console-linux-amd64'
$kfakeWin = Join-Path $kfakeDir 'kfake.exe'
$kfakeLinux = Join-Path $kfakeDir 'kfake'

function Test-SmartAppControlEnforced {
  try {
    $p = Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy' -ErrorAction Stop
    return [int]$p.VerifiedAndReputablePolicyState -eq 1
  } catch {
    return $false
  }
}

function ConvertTo-WslPath([string]$WinPath) {
  $out = & wsl -e wslpath -a $WinPath 2>$null
  if ($LASTEXITCODE -ne 0 -or -not $out) { throw "wslpath 실패: $WinPath" }
  return ($out | Select-Object -First 1).Trim()
}

function Build-Windows {
  Push-Location $kfakeDir
  try {
    & go build -o kfake.exe .
    if ($LASTEXITCODE) { throw 'kfake 빌드 실패' }
  } finally { Pop-Location }

  Push-Location $root
  try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    & go build -trimpath -tags production -ldflags '-H windowsgui' -o $winExe ./app
    if ($LASTEXITCODE) { throw 'kv-console 빌드 실패' }
  } finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
  }
}

function Build-Linux {
  if (-not (Get-Command wsl -ErrorAction SilentlyContinue)) {
    throw 'WSL 이 없습니다. Smart App Control 때문에 Windows exe 실행이 막히면 WSL 설치가 필요합니다.'
  }
  Push-Location $kfakeDir
  try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    & go build -o kfake .
    if ($LASTEXITCODE) { throw 'kfake(linux) 빌드 실패' }
  } finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
  }

  Push-Location $root
  try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    & go build -trimpath -o $linuxExe ./app
    if ($LASTEXITCODE) { throw 'kv-console(linux) 빌드 실패' }
  } finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
  }
  & wsl -e chmod +x (ConvertTo-WslPath $kfakeLinux) (ConvertTo-WslPath $linuxExe)
  if ($LASTEXITCODE) { throw 'chmod 실패' }
}

function Start-BgWin([string]$File, [string[]]$ArgumentList = @()) {
  $File = [IO.Path]::GetFullPath($File)
  if (-not (Test-Path -LiteralPath $File -PathType Leaf)) { throw "실행 파일 없음: $File" }
  Write-Host "[demo] 실행 시도(Windows): $File"
  $p = [Diagnostics.Process]::new()
  $p.StartInfo.FileName = $File
  $p.StartInfo.Arguments = ($ArgumentList -join ' ')
  $p.StartInfo.UseShellExecute = $false
  $p.StartInfo.CreateNoWindow = $true
  $p.StartInfo.WorkingDirectory = Split-Path -Parent $File
  try {
    if (-not $p.Start()) { throw "기동 실패: $File" }
    Write-Host "[demo] 실행 성공: PID=$($p.Id)"
    return @{ Kind = 'win'; Process = $p }
  } catch [ComponentModel.Win32Exception] {
    if ($_.Exception.NativeErrorCode -eq 4551) {
      Write-Host "[demo] Smart App Control 차단(4551): $File" -ForegroundColor Yellow
      return $null
    }
    throw
  }
}

function Start-BgWsl([string]$File, [string[]]$ArgumentList = @()) {
  $wslFile = ConvertTo-WslPath $File
  $argLine = (@($wslFile) + $ArgumentList | ForEach-Object {
      if ($_ -match '[\s"]') { '"' + ($_ -replace '"', '\"') + '"' } else { $_ }
    }) -join ' '
  Write-Host "[demo] 실행 시도(WSL): $argLine"
  $p = [Diagnostics.Process]::new()
  $p.StartInfo.FileName = 'wsl.exe'
  $p.StartInfo.Arguments = "-e sh -c `"$argLine`""
  $p.StartInfo.UseShellExecute = $false
  $p.StartInfo.CreateNoWindow = $true
  $p.StartInfo.WorkingDirectory = $root
  if (-not $p.Start()) { throw "WSL 기동 실패: $File" }
  Write-Host "[demo] WSL 실행 성공: PID=$($p.Id)"
  return @{ Kind = 'wsl'; Process = $p; WslPath = $wslFile }
}

function Stop-Bg($Handle) {
  if ($null -eq $Handle -or $null -eq $Handle.Process) { return }
  $p = $Handle.Process
  if ($Handle.Kind -eq 'wsl' -and $Handle.WslPath) {
    $base = [IO.Path]::GetFileName($Handle.WslPath)
    try { & wsl -e pkill -f $base 2>$null } catch {}
  }
  if (-not $p.HasExited) {
    try { $p.Kill() } catch {}
  }
}

& $kit -Action install
. (Join-Path $root '.tools\.build\activate.ps1')

$useWsl = [bool]$Wsl
if (-not $useWsl -and (Test-SmartAppControlEnforced)) {
  Write-Host '[demo] Smart App Control 강제 모드 감지 → WSL 리눅스 바이너리 사용' -ForegroundColor Yellow
  $useWsl = $true
}

$kfake = $null
$console = $null
try {
  if ($useWsl) {
    Build-Linux
    $kfake = Start-BgWsl $kfakeLinux
  } else {
    Build-Windows
    $kfake = Start-BgWin $kfakeWin
    if ($null -eq $kfake) {
      Write-Host '[demo] Windows kfake 차단 → WSL 폴백' -ForegroundColor Yellow
      $useWsl = $true
      Build-Linux
      $kfake = Start-BgWsl $kfakeLinux
    }
  }

  $deadline = (Get-Date).AddSeconds(25)
  while ((Get-Date) -lt $deadline) {
    try {
      $c = [Net.Sockets.TcpClient]::new()
      $c.Connect('127.0.0.1', 19092)
      $c.Dispose()
      break
    } catch {
      Start-Sleep -Milliseconds 200
    }
  }
  if ((Get-Date) -ge $deadline) { throw 'kfake 포트 19092 대기 시간 초과' }

  if ($useWsl) {
    $console = Start-BgWsl $linuxExe @('-brokers', $brokers, '-listen', $listen)
  } else {
    $console = Start-BgWin $winExe @('-brokers', $brokers, '-listen', $listen)
    if ($null -eq $console) {
      Write-Host '[demo] Windows 콘솔 차단 → 전체를 WSL 로 재기동' -ForegroundColor Yellow
      Stop-Bg $kfake
      $useWsl = $true
      Build-Linux
      $kfake = Start-BgWsl $kfakeLinux
      $deadline = (Get-Date).AddSeconds(25)
      while ((Get-Date) -lt $deadline) {
        try {
          $c = [Net.Sockets.TcpClient]::new()
          $c.Connect('127.0.0.1', 19092)
          $c.Dispose()
          break
        } catch { Start-Sleep -Milliseconds 200 }
      }
      if ((Get-Date) -ge $deadline) { throw 'kfake(WSL) 포트 19092 대기 시간 초과' }
      $console = Start-BgWsl $linuxExe @('-brokers', $brokers, '-listen', $listen)
    }
  }

  $ok = $false
  $deadline = (Get-Date).AddSeconds(25)
  while ((Get-Date) -lt $deadline) {
    try {
      $r = Invoke-RestMethod -Uri "http://$listen/api/cluster" -TimeoutSec 2
      if ($r.clusterId -or $r.brokers) {
        Write-Host ("[demo] /api/cluster OK  brokers={0} topics={1} groups={2}" -f @($r.brokers).Count, @($r.topics).Count, @($r.groups).Count)
        $ok = $true
        break
      }
    } catch {
      Start-Sleep -Milliseconds 300
    }
  }
  if (-not $ok) { throw '/api/cluster 스모크 실패' }

  if ($SmokeOnly) {
    Write-Host '[demo] smoke OK'
    return
  }
  $url = "http://$listen/"
  Write-Host "[demo] UI: $url   (Ctrl+C 로 종료)"
  if (-not $NoBrowser) { Start-Process $url }
  $console.Process.WaitForExit()
} finally {
  Stop-Bg $console
  Stop-Bg $kfake
}
