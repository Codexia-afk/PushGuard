$ErrorActionPreference = 'Stop'

# PushGuard binary installer. Override PUSHGUARD_RELEASE_BASE_URL for a release
# mirror or future product domain. The downloaded archive is always checked
# against the published SHA-256 manifest before installation.
$DefaultReleaseBase = 'https://github.com/Codexia-afk/PushGuard/releases/latest/download'
$ReleaseBase = if ($env:PUSHGUARD_RELEASE_BASE_URL) { $env:PUSHGUARD_RELEASE_BASE_URL } else { $DefaultReleaseBase }
$InstallDir = if ($env:PUSHGUARD_INSTALL_DIR) { $env:PUSHGUARD_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'PushGuard\bin' }

function Fail([string]$Message) {
    throw "PushGuard installer: $Message"
}

function Check-GoVersion {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Fail 'Go 1.22+ is required for a source installation.' }
    $GoVersion = (& go env GOVERSION).Trim()
    if ($GoVersion -notmatch '^go(?<major>\d+)\.(?<minor>\d+)') { Fail "Could not determine the Go version (found '$GoVersion')." }
    if ([int]$Matches.major -lt 1 -or ([int]$Matches.major -eq 1 -and [int]$Matches.minor -lt 22)) { Fail "Go 1.22+ is required (found $GoVersion)." }
}

function Print-Success {
    Write-Host "`nPushGuard Installer`n-------------------"
    & (Join-Path $InstallDir 'pushguard.exe') version
    Write-Host "`nInstallation complete.`n"
    Write-Host "Open any Git repository and run:`n`npushguard push`n"
    Write-Host "Verification only:`n`npushguard check"
}

if ($env:PUSHGUARD_INSTALL_FROM_SOURCE -eq '1') {
    Check-GoVersion
    $ProjectDir = if ($env:PUSHGUARD_SOURCE_DIR) { $env:PUSHGUARD_SOURCE_DIR } else { (Get-Location).Path }
    if (-not (Test-Path (Join-Path $ProjectDir 'go.mod')) -or -not (Test-Path (Join-Path $ProjectDir 'cmd\pushguard'))) { Fail 'source installation requires a PushGuard source directory.' }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $PreviousGobin = $env:GOBIN
    $PreviousGoFlags = $env:GOFLAGS
    try {
        $env:GOBIN = $InstallDir
        $ExistingGoFlags = if ($PreviousGoFlags) { $PreviousGoFlags } else { '' }
        $env:GOFLAGS = ($ExistingGoFlags + ' -buildvcs=false').Trim()
        Push-Location $ProjectDir
        try { go install ./cmd/pushguard; if ($LASTEXITCODE -ne 0) { Fail 'PushGuard build failed.' } }
        finally { Pop-Location }
    } finally {
        $env:GOBIN = $PreviousGobin
        $env:GOFLAGS = $PreviousGoFlags
    }
    $UserPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    if (($UserPath -split ';' | ForEach-Object { $_.TrimEnd('\') }) -notcontains $InstallDir.TrimEnd('\')) {
        [Environment]::SetEnvironmentVariable('Path', ($UserPath.TrimEnd(';') + ';' + $InstallDir), 'User')
    }
    $env:Path += ';' + $InstallDir
    Print-Success
    exit 0
}

if (-not ([Uri]$ReleaseBase).Scheme.Equals('https', [StringComparison]::OrdinalIgnoreCase)) { Fail 'release URL must use HTTPS.' }
if ($env:PUSHGUARD_RELEASE_VERSION -and $ReleaseBase -eq $DefaultReleaseBase) {
    $ReleaseBase = "https://github.com/Codexia-afk/PushGuard/releases/download/v$($env:PUSHGUARD_RELEASE_VERSION)"
}
$Architecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
switch ($Architecture.ToUpperInvariant()) {
    'AMD64' { $TargetArch = 'amd64' }
    'ARM64' { $TargetArch = 'arm64' }
    default { Fail "unsupported CPU architecture: $Architecture" }
}
$Asset = "pushguard-windows-$TargetArch.zip"
$TempDir = Join-Path ([IO.Path]::GetTempPath()) ("pushguard-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
try {
    $Archive = Join-Path $TempDir $Asset
    $Checksums = Join-Path $TempDir 'checksums.txt'
    Write-Host "PushGuard Installer`n-------------------`nPlatform: Windows $TargetArch`nDownloading PushGuard..."
    $Downloaded = $false
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$($ReleaseBase.TrimEnd('/'))/$Asset" -OutFile $Archive -ErrorAction Stop
        Invoke-WebRequest -UseBasicParsing -Uri "$($ReleaseBase.TrimEnd('/'))/checksums.txt" -OutFile $Checksums -ErrorAction Stop
        $Downloaded = $true
    } catch {
        if (Get-Command go -ErrorAction SilentlyContinue) {
            Write-Host "Pre-built binary archive not found. Compiling directly from source via Go..."
            New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
            & go install github.com/Codexia-afk/PushGuard/cmd/pushguard@latest
            $gobin = (& go env GOBIN 2>$null)
            if ($gobin) { $gobin = $gobin.Trim() }
            if (-not $gobin) { $gobin = Join-Path (& go env GOPATH).Trim() 'bin' }
            $SourceBin = Join-Path $gobin 'pushguard.exe'
            if (Test-Path $SourceBin) {
                Copy-Item -LiteralPath $SourceBin -Destination (Join-Path $InstallDir 'pushguard.exe') -Force
                $UserPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
                $PathEntries = @($UserPath -split ';' | Where-Object { $_ })
                if ($PathEntries -notcontains $InstallDir) {
                    [Environment]::SetEnvironmentVariable('Path', (($PathEntries + $InstallDir) -join ';'), 'User')
                }
                if (($env:Path -split ';') -notcontains $InstallDir) { $env:Path += ";$InstallDir" }
                Write-Host "✓ Compiled and installed in $InstallDir"
                Print-Success
                exit 0
            }
        }
        Fail "Could not download binary release from $($ReleaseBase.TrimEnd('/'))/$Asset. Install with: go install github.com/Codexia-afk/PushGuard/cmd/pushguard@latest"
    }
    $Expected = $null
    foreach ($Line in Get-Content $Checksums) {
        if ($Line -match '^\s*([0-9a-fA-F]{64})\s+\*?([^\s]+)\s*$' -and $Matches[2] -eq $Asset) {
            $Expected = $Matches[1].ToLowerInvariant()
            break
        }
    }
    if (-not $Expected) { Fail "checksums.txt does not contain $Asset" }
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $Archive).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) { Fail 'SHA-256 verification failed.' }
    Write-Host "✓ Downloaded`n✓ SHA-256 verified"
    Expand-Archive -LiteralPath $Archive -DestinationPath $TempDir -Force
    $Binary = Join-Path $TempDir 'pushguard.exe'
    if (-not (Test-Path $Binary)) { Fail 'release archive did not contain pushguard.exe' }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -LiteralPath $Binary -Destination (Join-Path $InstallDir 'pushguard.exe') -Force
    $UserPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    $PathEntries = @($UserPath -split ';' | Where-Object { $_ })
    if ($PathEntries -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable('Path', (($PathEntries + $InstallDir) -join ';'), 'User')
    }
    if (($env:Path -split ';') -notcontains $InstallDir) { $env:Path += ";$InstallDir" }
    Write-Host "✓ Installed in $InstallDir"
    Print-Success
} finally {
    Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
}
