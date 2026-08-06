# scripts/install.ps1 — one-line installer for sctl on Windows.
#
#   irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
#
# Downloads the hyphen-named release archive for this platform, verifies its
# sha256 against checksums.txt from the same release, expands it, and installs
# sctl.exe (overwriting any existing file) into SCTL_INSTALL_DIR.
#
# Environment:
#   SCTL_VERSION       version to install (leading "v" accepted); default: latest release
#   SCTL_INSTALL_DIR   install directory; default: $env:LOCALAPPDATA\sctl\bin
#   SCTL_LATEST_API    (test seam) latest-release JSON endpoint
#   SCTL_DOWNLOAD_BASE (test seam) base URL for archive + checksums.txt downloads
#
# Requires: Windows PowerShell 5.1+ or PowerShell 7.

$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 on older Windows defaults to TLS 1.0/1.1, which GitHub
# rejects; widen the protocol set before any HTTPS request.
if ($PSVersionTable.PSVersion.Major -lt 6) {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("sctl-install-" + [guid]::NewGuid().ToString('N'))

$LatestApi = $env:SCTL_LATEST_API
if ([string]::IsNullOrEmpty($LatestApi)) { $LatestApi = 'https://api.github.com/repos/scriptscat/sctl/releases/latest' }
$DownloadBase = $env:SCTL_DOWNLOAD_BASE
if ([string]::IsNullOrEmpty($DownloadBase)) { $DownloadBase = 'https://github.com/scriptscat/sctl/releases/download' }

function Download-File([string]$Url, [string]$OutFile) {
  # PS 5.1 needs -UseBasicParsing; it is ignored-or-removed on PowerShell 7.
  if ($PSVersionTable.PSVersion.Major -lt 6) {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing | Out-Null
  } else {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile | Out-Null
  }
}

try {
  New-Item -ItemType Directory -Path $TempDir | Out-Null

  # 1. Detect platform.
  $Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'x86_64' }
    'ARM64' { 'arm64' }
  }
  if (-not $Arch) {
    Write-Host "error: unsupported architecture '$env:PROCESSOR_ARCHITECTURE' - expected AMD64 or ARM64"
    exit 1
  }

  # 2. Resolve version.
  if ($env:SCTL_VERSION) {
    $Ver = $env:SCTL_VERSION.TrimStart('v')
  } else {
    $latest = Invoke-RestMethod -Uri $LatestApi -Headers @{ 'User-Agent' = 'sctl-install' }
    $Ver = ([string]$latest.tag_name).TrimStart('v')
  }
  if ([string]::IsNullOrEmpty($Ver)) {
    Write-Host 'error: could not determine sctl version to install'
    exit 1
  }

  # 3. Download.
  $Archive = "sctl-$Ver-windows-$Arch.zip"
  $ArchiveUrl = "$DownloadBase/v$Ver/$Archive"
  $ChecksumsUrl = "$DownloadBase/v$Ver/checksums.txt"
  Write-Host "downloading $Archive"
  $ArchiveFile = Join-Path $TempDir $Archive
  $ChecksumsFile = Join-Path $TempDir 'checksums.txt'
  Download-File -Url $ArchiveUrl -OutFile $ArchiveFile
  Download-File -Url $ChecksumsUrl -OutFile $ChecksumsFile

  # 4. Verify sha256 against the matching checksums.txt line.
  $expected = $null
  foreach ($line in Get-Content -Path $ChecksumsFile) {
    if ($line -match "^\s*([0-9a-fA-F]{64})\s+\*?([^\s]+)\s*$") {
      if ($matches[2] -eq $Archive) { $expected = $matches[1]; break }
    }
  }
  if ([string]::IsNullOrEmpty($expected)) {
    Write-Host "error: no checksum for $Archive in checksums.txt"
    exit 1
  }
  $actual = (Get-FileHash -Algorithm SHA256 -Path $ArchiveFile).Hash.ToLowerInvariant()
  $expected = $expected.ToLowerInvariant()
  if ($actual -ne $expected) {
    Write-Host "error: checksum mismatch for $Archive"
    Write-Host "  expected: $expected"
    Write-Host "  actual:   $actual"
    exit 1
  }
  Write-Host "verified sha256 ($Archive): $expected"

  # 5. Expand; sctl.exe sits in the sctl-<ver>-windows-<arch>/ directory.
  $ExtractDir = Join-Path $TempDir 'extract'
  Expand-Archive -Path $ArchiveFile -DestinationPath $ExtractDir
  $Exe = Join-Path $ExtractDir "sctl-$Ver-windows-$Arch\sctl.exe"
  if (-not (Test-Path -Path $Exe -PathType Leaf)) {
    Write-Host "error: sctl.exe not found in archive (expected $Exe)"
    exit 1
  }

  # 6. Install, overwriting any existing sctl.exe.
  $InstallDir = $env:SCTL_INSTALL_DIR
  if ([string]::IsNullOrEmpty($InstallDir)) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'sctl\bin'
  }
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $Installed = Join-Path $InstallDir 'sctl.exe'
  Copy-Item -Path $Exe -Destination $Installed -Force
  Write-Host "installed sctl to $Installed"

  # 7. Report: install path, version, and a setx PATH hint when needed.
  & $Installed version
  if ($LASTEXITCODE -ne 0) {
    Write-Host "warning: installed, but 'sctl version' failed (exit code $LASTEXITCODE)"
  }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $onPath = $false
  foreach ($entry in ($userPath -split ';')) {
    if ($entry -and $entry.TrimEnd('\') -eq $InstallDir.TrimEnd('\')) { $onPath = $true; break }
  }
  if (-not $onPath) {
    # Append to the *user* PATH only: setx silently truncates values over 1024
    # chars, so echoing $env:Path (the merged machine+user PATH) back into the
    # user PATH would both duplicate machine entries and risk truncation.
    $hintPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
    Write-Host ''
    Write-Host "$InstallDir is not on your user PATH. Add it by running:"
    Write-Host ''
    Write-Host "  setx PATH `"$hintPath`""
    Write-Host ''
    Write-Host 'sctl will not modify your PATH for you.'
  }
} catch {
  Write-Host "error: $($_.Exception.Message)"
  exit 1
} finally {
  Remove-Item -Path $TempDir -Recurse -Force -ErrorAction SilentlyContinue
}
