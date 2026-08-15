# OctoPort installer for Windows (PowerShell)
# Fetches the latest platform binary/package from GitHub releases and installs it.
#
# Run directly:
#   powershell -ExecutionPolicy Bypass -File install.ps1 [OPTIONS]
#
# Or as a one-liner from PowerShell (no git/bash needed):
#   iex (irm https://octoport.itanishq.space/install.ps1)
#
# Configuration via environment variables (handy for the one-liner):
#   OCTOPORT_REPO          GitHub repository URL (default: https://github.com/047pegasus/octoport)
#   OCTOPORT_VERSION       Version tag (e.g. v0.2.0) or "latest" (default: latest)
#   OCTOPORT_INSTALL_DIR   Install directory for the CLI/GUI binaries (default: %LOCALAPPDATA%\octoport)
#   OCTOPORT_INSTALL_CLI   "true"/"false" (default: true)
#   OCTOPORT_INSTALL_GUI   "true"/"false" (default: true)
#
# Release assets follow the naming convention:
#   CLI: octoport-windows-x86_64.msi, octoport-windows-x86_64.exe
#   GUI: octoport-app-windows-x86_64.msi, octoport-app-windows-x86_64.exe
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 defaults to TLS 1.0/1.1, which GitHub rejects.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

# --- configuration -----------------------------------------------------------
$global:OctoRepo       = if ($env:OCTOPORT_REPO)        { $env:OCTOPORT_REPO }        else { 'https://github.com/047pegasus/octoport' }
$global:OctoVersion    = if ($env:OCTOPORT_VERSION)     { $env:OCTOPORT_VERSION }     else { 'latest' }
$global:OctoDestDir    = if ($env:OCTOPORT_INSTALL_DIR) { $env:OCTOPORT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'octoport' }
$global:OctoInstallCLI = if ($env:OCTOPORT_INSTALL_CLI) { $env:OCTOPORT_INSTALL_CLI -eq 'true' } else { $true }
$global:OctoInstallGUI = if ($env:OCTOPORT_INSTALL_GUI) { $env:OCTOPORT_INSTALL_GUI -eq 'true' } else { $true }
$global:OctoUninstall  = $false

# --- progress UI -------------------------------------------------------------
# ONE persistent spinner runs for the entire install, pinned to the bottom line
# while every log message, download and install step executes. The only thing
# that pauses it is the curl progress bar (downloads). Coordination happens via
# a pause flag file (never signals the thread), so it cannot deadlock. When
# output is redirected (piped) the spinner disables itself.
$global:OctoSpinnerOn        = $false
$global:OctoSpinnerThread    = $null
$global:OctoSpinnerLabelFile = ''
$global:OctoSpinnerPauseFile = ''
$global:OctoSpinnerStopFile  = ''

function Start-OctoSpinner {
    param([string]$Label = 'working')
    if ([Console]::IsOutputRedirected) { return }
    try {
        # Control files live in $env:TEMP. The thread discovers them by glob so
        # it needs no shared state with the main script (a ThreadStart runs in
        # its own runspace and cannot see our $global: variables).
        $global:OctoSpinnerLabelFile = Join-Path $env:TEMP ('octoport-label-' + [guid]::NewGuid().ToString('N') + '.txt')
        $global:OctoSpinnerPauseFile = Join-Path $env:TEMP ('octoport-pause-'  + [guid]::NewGuid().ToString('N') + '.txt')
        $global:OctoSpinnerStopFile  = Join-Path $env:TEMP ('octoport-stop-'   + [guid]::NewGuid().ToString('N') + '.txt')
        Set-Content -LiteralPath $global:OctoSpinnerLabelFile -Value $Label -Encoding ascii
        $ts = [System.Threading.ThreadStart] {
            $frames = @('⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏')
            $i = 0
            $labelGlob = Join-Path $env:TEMP 'octoport-label-*.txt'
            $pauseGlob = Join-Path $env:TEMP 'octoport-pause-*.txt'
            $stopGlob  = Join-Path $env:TEMP 'octoport-stop-*.txt'
            while (-not (Get-ChildItem -Path $stopGlob -ErrorAction SilentlyContinue)) {
                if (-not (Get-ChildItem -Path $pauseGlob -ErrorAction SilentlyContinue)) {
                    $label = 'working'
                    $labelFile = Get-ChildItem -Path $labelGlob -ErrorAction SilentlyContinue | Select-Object -First 1
                    if ($labelFile) {
                        $label = (Get-Content -LiteralPath $labelFile.FullName -Raw -ErrorAction SilentlyContinue).Trim()
                    }
                    [Console]::SetCursorPosition(0, [Console]::CursorTop)
                    $width = [Math]::Max(20, [Console]::WindowWidth)
                    [Console]::Write(('  {0} {1}...' -f $frames[$i % 10], $label).PadRight($width))
                    $i++
                }
                Start-Sleep -Milliseconds 100
            }
            try {
                [Console]::SetCursorPosition(0, [Console]::CursorTop)
                [Console]::Write(('' ).PadRight([Math]::Max(20, [Console]::WindowWidth)))
            } catch { }
        }
        $global:OctoSpinnerThread = [System.Threading.Thread]::new($ts)
        $global:OctoSpinnerThread.IsBackground = $true
        $global:OctoSpinnerThread.Start()
        $global:OctoSpinnerOn = $true
    } catch {
        $global:OctoSpinnerOn = $false
    }
}

function Set-OctoSpinnerLabel {
    param([string]$Label)
    if (-not $global:OctoSpinnerOn) { return }
    Set-Content -LiteralPath $global:OctoSpinnerLabelFile -Value $Label -Encoding ascii -ErrorAction SilentlyContinue
}

function Invoke-OctoSpinnerPause {
    if (-not $global:OctoSpinnerOn) { return }
    Set-Content -LiteralPath $global:OctoSpinnerPauseFile -Value '' -Encoding ascii
    Start-Sleep -Milliseconds 150
}

function Invoke-OctoSpinnerResume {
    if (-not $global:OctoSpinnerOn) { return }
    Remove-Item -LiteralPath $global:OctoSpinnerPauseFile -Force -ErrorAction SilentlyContinue
}

function Stop-OctoSpinner {
    if (-not $global:OctoSpinnerOn) { return }
    New-Item -ItemType File -Path $global:OctoSpinnerStopFile -Force | Out-Null
    if ($global:OctoSpinnerThread) { $global:OctoSpinnerThread.Join(500) | Out-Null }
    Remove-Item -LiteralPath $global:OctoSpinnerStopFile, $global:OctoSpinnerPauseFile, $global:OctoSpinnerLabelFile -Force -ErrorAction SilentlyContinue
    try {
        [Console]::SetCursorPosition(0, [Console]::CursorTop)
        [Console]::Write(('' ).PadRight([Math]::Max(20, [Console]::WindowWidth)) + "`r")
    } catch { }
    $global:OctoSpinnerOn = $false
}

# Print a log line above the spinner (pause -> clear status line -> write
# message -> resume). Falls back to plain output when the spinner is off.
function Write-OctoLog {
    param([string]$Message)
    if ($global:OctoSpinnerOn) {
        Invoke-OctoSpinnerPause
        try {
            [Console]::SetCursorPosition(0, [Console]::CursorTop)
            [Console]::Write(('' ).PadRight([Math]::Max(20, [Console]::WindowWidth)) + "`r$Message`n")
        } catch {
            Write-Host $Message
        }
        Invoke-OctoSpinnerResume
    } else {
        Write-Host $Message
    }
}

# Same as Write-OctoLog but routed to stderr (errors / warnings).
function Write-OctoWarn {
    param([string]$Message)
    if ($global:OctoSpinnerOn) {
        Invoke-OctoSpinnerPause
        try {
            [Console]::SetCursorPosition(0, [Console]::CursorTop)
            [Console]::Write(('' ).PadRight([Math]::Max(20, [Console]::WindowWidth)) + "`r")
            [Console]::Error.WriteLine($Message)
        } catch {
            Write-Host $Message -ForegroundColor Yellow
        }
        Invoke-OctoSpinnerResume
    } else {
        Write-Host $Message -ForegroundColor Yellow
    }
}

# --- helpers -----------------------------------------------------------------
function Get-OctoOS {
    if ($PSVersionTable.PSEdition -eq 'Core') {
        if ($IsWindows) { return 'windows' }
        if ($IsLinux)   { return 'linux' }
        if ($IsMacOS)   { return 'macos' }
        throw 'unsupported-os'
    }
    return 'windows'
}

function Get-OctoArch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'x86_64' }
        'ARM64' { return 'aarch64' }
        default { throw "unsupported-arch: $env:PROCESSOR_ARCHITECTURE" }
    }
}

# Download with curl's own percentage bar. The spinner is paused for the whole
# transfer so the bar renders cleanly, then resumed.
function Get-OctoDownload {
    param([string]$Url, [string]$Out)
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($curl) {
        if ($global:OctoSpinnerOn) { Invoke-OctoSpinnerPause }
        try {
            & curl.exe -fSL --connect-timeout 15 --max-time 600 --progress-bar $Url -o $Out
            if ($LASTEXITCODE -ne 0) {
                throw "curl failed with exit code $LASTEXITCODE downloading $Url"
            }
        } finally {
            if ($global:OctoSpinnerOn) { Invoke-OctoSpinnerResume }
        }
    } else {
        if ($global:OctoSpinnerOn) { Invoke-OctoSpinnerPause }
        try {
            Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
        } finally {
            if ($global:OctoSpinnerOn) { Invoke-OctoSpinnerResume }
        }
    }
}

# Run an install step under the persistent spinner: retitle the status line and
# run the action in the foreground while the spinner keeps turning.
function Invoke-OctoRunWithSpinner {
    param([string]$Label, [scriptblock]$Action)
    Set-OctoSpinnerLabel $Label
    & $Action
}

# Look up the expected sha256 for an asset in the downloaded SHA256SUMS file.
function Get-OctoExpectedChecksum {
    param([string]$AssetName)
    $pattern = '\s+' + [regex]::Escape($AssetName) + '\s*$'
    $line = Get-Content -LiteralPath $global:OctoChecksumsFile |
        Where-Object { $_ -match $pattern } |
        Select-Object -First 1
    if ($null -eq $line) { return $null }
    return ($line -split '\s+')[0].Trim()
}

# Download an asset, verify its checksum, then copy it to dest_path. Falls back
# to a UAC-elevated copy when the destination directory isn't writable.
function Invoke-OctoVerifyInstallBinary {
    param([string]$AssetName, [string]$DestPath)
    $expected = Get-OctoExpectedChecksum $AssetName
    if (-not $expected) {
        Write-OctoWarn "! no checksum published for $AssetName; aborting"
        throw "no checksum for $AssetName"
    }

    Write-OctoLog "Downloading $AssetName..."
    $assetPath = Join-Path $global:OctoTmp $AssetName
    Get-OctoDownload "$($global:OctoBaseUrl)/$AssetName" $assetPath

    $actual = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        Write-OctoWarn "! checksum mismatch for $AssetName - refusing to install"
        Write-OctoWarn "  expected $expected"
        Write-OctoWarn "  actual   $actual"
        throw "checksum mismatch for $AssetName"
    }
    Write-OctoLog '  checksum ok'

    $destDir = Split-Path -Parent $DestPath
    if (-not (Test-Path -LiteralPath $destDir)) {
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
    }

    # Probe write access without elevation.
    $canWrite = $false
    try {
        $probe = Join-Path $destDir '.octoport-write-probe'
        [System.IO.File]::WriteAllText($probe, '')
        Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
        $canWrite = $true
    } catch {
        $canWrite = $false
    }

    if ($canWrite) {
        Invoke-OctoRunWithSpinner "installing $AssetName" {
            Copy-Item -LiteralPath $assetPath -Destination $DestPath -Force
        }
    } else {
        Write-OctoWarn "! $DestPath requires elevated privileges; requesting elevation"
        Invoke-OctoSpinnerPause
        try {
            $copyArgs = '/c copy /y "' + $assetPath + '" "' + $DestPath + '"'
            $p = Start-Process -FilePath 'cmd.exe' -Verb RunAs -ArgumentList $copyArgs -Wait -PassThru
            if ($p.ExitCode -ne 0) {
                throw "elevated copy failed with exit code $($p.ExitCode)"
            }
        } finally {
            Invoke-OctoSpinnerResume
        }
    }
}

function Install-OctoMsi {
    param([string]$Asset)
    $assetPath = Join-Path $global:OctoTmp $Asset
    Write-OctoLog "Installing .msi: $Asset"
    Invoke-OctoSpinnerPause
    try {
        $p = Start-Process -FilePath 'msiexec.exe' -ArgumentList @('/i', $assetPath, '/quiet', '/norestart') -Wait -PassThru
        if ($p.ExitCode -ne 0 -and $p.ExitCode -ne 3010) {
            throw "msiexec failed with exit code $($p.ExitCode)"
        }
    } finally {
        Invoke-OctoSpinnerResume
    }
}

function Add-OctoToPath {
    $path = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $path) { $path = '' }
    if ($path -notmatch [regex]::Escape($global:OctoDestDir)) {
        $newPath = $global:OctoDestDir + ';' + $path
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path = $global:OctoDestDir + ';' + $env:Path
        Write-OctoLog "  added $($global:OctoDestDir) to your PATH (open a new terminal to use octoport)"
    }
}

function Remove-OctoFromPath {
    $path = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($path) {
        $newPath = ($path -split ';' | Where-Object { $_ -and $_ -ne $global:OctoDestDir }) -join ';'
        if ($newPath -ne $path) {
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        }
    }
}

function Show-OctoUsage {
    Write-Host @'
OctoPort installer for Windows

Usage: install.ps1 [OPTIONS]

Options:
  --cli-only       Install only the CLI
  --gui-only       Install only the GUI
  --both           Install both CLI and GUI (default)
  --uninstall      Uninstall OctoPort (CLI, GUI, or both)
  --version VER    Version to install (default: latest)
  --repo URL       GitHub repo URL (default: https://github.com/047pegasus/octoport)
  --dest DIR       Install directory for CLI/GUI binaries (default: %LOCALAPPDATA%\octoport)
  -h, --help       Show this help

Run as a one-liner from PowerShell (recommended):
  iex (irm https://octoport.itanishq.space/install.ps1)

Customize the one-liner with environment variables:
  $env:OCTOPORT_REPO="https://github.com/yourorg/octoport"; $env:OCTOPORT_VERSION="v0.2.0"; iex (irm https://octoport.itanishq.space/install.ps1)
'@
}

# --- parse args --------------------------------------------------------------
$argsList = @($args)
for ($i = 0; $i -lt $argsList.Count; $i++) {
    switch ($argsList[$i]) {
        '--cli-only'  { $global:OctoInstallCLI = $true;  $global:OctoInstallGUI = $false }
        '--gui-only'  { $global:OctoInstallCLI = $false; $global:OctoInstallGUI = $true }
        '--both'      { $global:OctoInstallCLI = $true;  $global:OctoInstallGUI = $true }
        '--uninstall' { $global:OctoUninstall = $true }
        '--version'   { $i++; if ($i -lt $argsList.Count) { $global:OctoVersion = $argsList[$i] } }
        '--repo'      { $i++; if ($i -lt $argsList.Count) { $global:OctoRepo = $argsList[$i] } }
        '--dest'      { $i++; if ($i -lt $argsList.Count) { $global:OctoDestDir = $argsList[$i] } }
        '-h'          { Show-OctoUsage; return }
        '--help'      { Show-OctoUsage; return }
        default {
            Write-Host "Unknown option: $($argsList[$i])" -ForegroundColor Yellow
            Show-OctoUsage
            return
        }
    }
}

# --- version path ------------------------------------------------------------
$global:OctoOS  = Get-OctoOS
$global:OctoArch = Get-OctoArch
if ($global:OctoOS -ne 'windows') {
    Write-OctoWarn "! this PowerShell installer targets Windows; on $($global:OctoOS) use install.sh instead"
    return
}
if ($global:OctoVersion -eq 'latest') {
    $global:OctoBaseUrl     = "$($global:OctoRepo)/releases/latest/download"
    $global:OctoChecksumsUrl = "$($global:OctoRepo)/releases/latest/download/SHA256SUMS"
} else {
    $global:OctoBaseUrl     = "$($global:OctoRepo)/releases/download/$($global:OctoVersion)"
    $global:OctoChecksumsUrl = "$($global:OctoRepo)/releases/download/$($global:OctoVersion)/SHA256SUMS"
}

# --- main --------------------------------------------------------------------
Start-OctoSpinner 'initializing'
$global:OctoTmp = Join-Path $env:TEMP ('octoport-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $global:OctoTmp -Force | Out-Null
try {
    if ($global:OctoUninstall) {
        Write-OctoLog 'Uninstalling OctoPort...'

        if ($global:OctoInstallCLI) {
            Write-OctoLog 'Removing CLI...'
            Remove-Item -LiteralPath (Join-Path $global:OctoDestDir 'octoport.exe') -Force -ErrorAction SilentlyContinue
            # Best-effort MSI removal via the registered product.
            $pkg = Get-Package -Name 'octoport' -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($pkg) {
                Invoke-OctoSpinnerPause
                try {
                    Start-Process -FilePath 'msiexec.exe' -ArgumentList @('/x', $pkg.FastPackageReference, '/quiet', '/norestart') -Wait | Out-Null
                } finally {
                    Invoke-OctoSpinnerResume
                }
            }
            Write-OctoLog '  CLI removed'
        }
        if ($global:OctoInstallGUI) {
            Write-OctoLog 'Removing GUI...'
            Remove-Item -LiteralPath (Join-Path $global:OctoDestDir 'octoport-app.exe') -Force -ErrorAction SilentlyContinue
            $pkg = Get-Package -Name 'octoport-app' -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($pkg) {
                Invoke-OctoSpinnerPause
                try {
                    Start-Process -FilePath 'msiexec.exe' -ArgumentList @('/x', $pkg.FastPackageReference, '/quiet', '/norestart') -Wait | Out-Null
                } finally {
                    Invoke-OctoSpinnerResume
                }
            }
            Write-OctoLog '  GUI removed'
        }

        # Config directories (mirrors install.sh).
        $configDirs = @(
            (Join-Path $env:USERPROFILE '.config\octoport'),
            (Join-Path $env:USERPROFILE '.config\octoport-app'),
            (Join-Path $env:USERPROFILE '.local\share\octoport'),
            (Join-Path $env:USERPROFILE '.local\share\octoport-app')
        )
        foreach ($d in $configDirs) {
            Remove-Item -LiteralPath $d -Recurse -Force -ErrorAction SilentlyContinue
        }

        Remove-OctoFromPath
        Write-OctoLog 'OctoPort uninstalled.'
    }
    else {
        Write-OctoLog "Installing OctoPort $($global:OctoVersion) ($($global:OctoOS)/$($global:OctoArch)) from $($global:OctoRepo)"

        # Fetch SHA256SUMS once.
        $global:OctoChecksumsFile = Join-Path $global:OctoTmp 'SHA256SUMS'
        Write-OctoLog 'Fetching checksums...'
        Get-OctoDownload $global:OctoChecksumsUrl $global:OctoChecksumsFile

        if ($global:OctoInstallCLI) {
            Write-OctoLog '=== Installing CLI ==='
            $asset = "octoport-$($global:OctoOS)-$($global:OctoArch).msi"
            if (Get-OctoExpectedChecksum $asset) {
                Get-OctoDownload "$($global:OctoBaseUrl)/$asset" (Join-Path $global:OctoTmp $asset)
                Install-OctoMsi $asset
            } else {
                $asset = "octoport-$($global:OctoOS)-$($global:OctoArch).exe"
                Invoke-OctoVerifyInstallBinary $asset (Join-Path $global:OctoDestDir 'octoport.exe')
            }
        }

        if ($global:OctoInstallGUI) {
            Write-OctoLog '=== Installing GUI ==='
            $asset = "octoport-app-$($global:OctoOS)-$($global:OctoArch).msi"
            if (Get-OctoExpectedChecksum $asset) {
                Get-OctoDownload "$($global:OctoBaseUrl)/$asset" (Join-Path $global:OctoTmp $asset)
                Install-OctoMsi $asset
            } else {
                $asset = "octoport-app-$($global:OctoOS)-$($global:OctoArch).exe"
                Invoke-OctoVerifyInstallBinary $asset (Join-Path $global:OctoDestDir 'octoport-app.exe')
            }
        }

        Add-OctoToPath

        Stop-OctoSpinner
        Write-Host ''
        Write-Host 'Installation complete.'
        if ($global:OctoInstallCLI) {
            Write-Host '  CLI: octoport login && octoport expose 3000'
        }
        if ($global:OctoInstallGUI) {
            Write-Host '  GUI: octoport-app (or run from Start menu)'
        }
    }
}
finally {
    Stop-OctoSpinner
    Remove-Item -LiteralPath $global:OctoTmp -Recurse -Force -ErrorAction SilentlyContinue
}
