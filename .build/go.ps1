#requires -Version 5.1
# Facade adapter: selects the language, delegates the menu and all actions to setup.ps1.
[CmdletBinding()]
param(
 [ValidateSet('menu','install','configure','update','reset','verify','prime','offline-check','env','global-on','global-off','remove','list','run','emulator-install','emulator-list','emulator-start','emulator-remove','vue-init','tauri-install','tauri-verify','tauri-prime','tauri-offline-check','tauri-remove')][string]$Action='menu',
 [string]$Project='', [int]$AndroidApi=36, [string]$BuildTools='36.0.0',
 [string]$NdkVersion='27.2.12479018', [switch]$WithNdk, [switch]$WithAdb,
 [switch]$AcceptAndroidLicenses, [string]$Task='assembleDebug', [int]$JdkMajor=21,
 [string]$PythonVersion='3.13', [string]$GoVersion='1.27.2', [int]$NodeMajor=24,
 [string]$DotnetChannel='10.0', [ValidateSet('msvc','gnu')][string]$RustAbi='msvc',
 [ValidateSet('x86_64','arm64-v8a')][string]$EmulatorAbi='x86_64',
 [ValidateSet('default','google_apis')][string]$EmulatorFlavor='google_apis',
 [ValidatePattern('^[A-Za-z][A-Za-z0-9_.-]{0,63}$')][string]$AvdName='pbk_android',
 [switch]$Headless, [switch]$PurgeEmulatorPackages,
 [ValidateSet('verify','env','global-on','global-off','prime','offline-check','list','run')][string]$Mode='verify',
 [ValidateSet('none','emulator','tauri','vue')][string]$Feature='none',
 [ValidateSet('','install','verify','prime','offline-check','list','start','remove','init')][string]$Operation='',
 [ValidateSet('all','env','tools')][string]$Scope='all', [switch]$Yes,
 [string[]]$CommandArgs=@()
)
$forward=@{ Language='go' }
foreach ($item in $PSBoundParameters.GetEnumerator()) { $forward[$item.Key]=$item.Value }
$forward['Action']=$Action
& "$PSScriptRoot/setup.ps1" @forward
if (-not $?) { exit 1 }
