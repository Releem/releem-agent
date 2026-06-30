<#
.SYNOPSIS
    Releem MySQL Configurer for Windows.

.DESCRIPTION
    Configures, applies, rolls back, or updates MySQL configuration
    as recommended by the Releem Agent.
    Deployed to C:\Program Files\ReleemAgent\mysqlconfigurer.ps1

.PARAMETER Apply
    Apply the recommended MySQL configuration (with user confirmation).

.PARAMETER NonInteractive
    Apply the recommended MySQL configuration non-interactively.

.PARAMETER NoRestart
    Copy the recommended MySQL configuration without restarting MySQL.

.PARAMETER QueueApply
    Ask the Releem Agent to create an apply_config job instead of applying locally.

.PARAMETER Rollback
    Restore the previous MySQL configuration from backup.

.PARAMETER Configure
    Enable Performance Schema and slow query log in MySQL.

.PARAMETER Update
    Refresh the installer script and delegate the full agent update flow.

.PARAMETER ApiKey
    Releem API key (overrides value from releem.conf).

.EXAMPLE
    .\mysqlconfigurer.ps1 -Configure
    .\mysqlconfigurer.ps1 -Apply
    .\mysqlconfigurer.ps1 -Apply -NonInteractive
    .\mysqlconfigurer.ps1 -Apply -NonInteractive -NoRestart
    .\mysqlconfigurer.ps1 -QueueApply
    .\mysqlconfigurer.ps1 -Rollback
    .\mysqlconfigurer.ps1 -Update
#>

[CmdletBinding()]
param(
    [Alias('a')]
    [switch]$Apply,
    [Alias('Automatic')]
    [switch]$NonInteractive,
    [switch]$NoRestart,
    [switch]$QueueApply,
    [Alias('r')]
    [switch]$Rollback,
    [Alias('p')]
    [switch]$Configure,
    [Alias('u')]
    [switch]$Update,
    [Alias('k')]
    [string]$ApiKey
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Script version
# ---------------------------------------------------------------------------

$ScriptVersion = '1.23.7'

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

$ConfigFilePath   = 'C:\ProgramData\ReleemAgent\releem.conf'
$ReleemConfDir    = 'C:\ProgramData\ReleemAgent\conf.d\'
$DbConfigFileName = 'z_aiops_mysql.cnf'
$StagingCnfPath   = "${ReleemConfDir}${DbConfigFileName}"
$BackupCnfPath    = "${ReleemConfDir}${DbConfigFileName}.bkp"
$LogFilePath      = 'C:\ProgramData\ReleemAgent\releem-mysqlconfigurer.log'
$AgentBinaryPath  = if ($env:RELEEM_AGENT_BINARY_PATH) { $env:RELEEM_AGENT_BINARY_PATH } else { 'C:\Program Files\ReleemAgent\releem-agent.exe' }
$InstallerScriptPath = 'C:\Program Files\ReleemAgent\install.ps1'
$DbVersionFilePath = "${ReleemConfDir}DB_Version.txt"
$CurrentVersionUrl = if ($env:RELEEM_CURRENT_VERSION_URL) { $env:RELEEM_CURRENT_VERSION_URL } else { 'https://releem.s3.us-east-1.amazonaws.com/v2/current_version_agent' }
$InstallerScriptUrl = if ($env:RELEEM_INSTALLER_SCRIPT_URL) { $env:RELEEM_INSTALLER_SCRIPT_URL } else { 'https://releem.s3.us-east-1.amazonaws.com/v2/install.ps1' }

# ---------------------------------------------------------------------------
# Helper: Write-Log
# Appends a timestamped message to $LogFilePath and writes to console.
# ---------------------------------------------------------------------------

function Write-Log {
    param([string]$Message)
    $timestamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
    $line = "[$timestamp] $Message"
    Write-Host $line
    Add-Content -Path $LogFilePath -Value $line
}

# ---------------------------------------------------------------------------
# Helper: Read-ReleemConfig
# Reads C:\ProgramData\ReleemAgent\releem.conf key=value pairs into a hashtable.
# Returns empty hashtable if file not found.
# ---------------------------------------------------------------------------

function Read-ReleemConfig {
    $config = @{}
    if (-not (Test-Path $ConfigFilePath)) {
        return $config
    }
    foreach ($line in (Get-Content -Path $ConfigFilePath)) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
        $idx = $trimmed.IndexOf('=')
        if ($idx -gt 0) {
            $key   = $trimmed.Substring(0, $idx).Trim()
            $value = $trimmed.Substring($idx + 1).Trim()
            # Strip surrounding double quotes (releem.conf stores values as "value")
            if ($value.Length -ge 2 -and $value[0] -eq '"' -and $value[-1] -eq '"') {
                $value = $value.Substring(1, $value.Length - 2)
            }
            $config[$key] = $value
        }
    }
    return $config
}

# ---------------------------------------------------------------------------
# Helper: Set-ReleemConfigValue
# Adds or updates a single key="value" line in releem.conf.
# ---------------------------------------------------------------------------

function Set-ReleemConfigValue {
    param([string]$Key, [string]$Value)
    $lines = if (Test-Path $ConfigFilePath) {
        (Get-Content -Path $ConfigFilePath) | Where-Object { $_ -notmatch "^$([regex]::Escape($Key))=" }
    } else { @() }
    $lines += "$Key=`"$Value`""
    $noBomUtf8 = New-Object System.Text.UTF8Encoding $false
    [System.IO.File]::WriteAllText($ConfigFilePath, ($lines -join "`r`n") + "`r`n", $noBomUtf8)
}

# ---------------------------------------------------------------------------
# Helper: Send-ConfigurerLog
# Uploads log contents to the Releem API on every exit.
# Uses Write-Host (not Write-Log) to avoid recursion if log dir is missing.
# ---------------------------------------------------------------------------

function Send-ConfigurerLog {
    if (-not $apikey) {
        Write-Host 'INFO: No API key available; skipping configurer log upload.'
        return
    }

    $logUrl = "$ApiBaseUrl/v2/events/configurer_log"

    try {
        $logContent = if (Test-Path $LogFilePath) { Get-Content -Path $LogFilePath -Raw } else { '' }
        $headers = @{ 'x-releem-api-key' = $apikey }
        Invoke-RestMethod -Uri $logUrl -Method Post -Body $logContent -Headers $headers -ErrorAction Stop | Out-Null
        Write-Host 'Configurer log uploaded to Releem API.'
    } catch {
        Write-Host "WARNING: Failed to upload configurer log to Releem API: $_"
    }
}

# ---------------------------------------------------------------------------
# Helper: Find-MysqlExe
# Searches standard locations for mysql.exe; returns full path or $null.
# ---------------------------------------------------------------------------

function Find-MysqlExe {
    # (1) Check PATH
    $cmd = Get-Command mysql.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    # (2) MySQL Server in Program Files
    $item = Get-Item 'C:\Program Files\MySQL\MySQL Server *\bin\mysql.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($item) { return $item.FullName }

    # (3) MariaDB in Program Files
    $item = Get-Item 'C:\Program Files\MariaDB*\bin\mysql.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($item) { return $item.FullName }

    # (4) MySQL Server in Program Files (x86)
    $item = Get-Item 'C:\Program Files (x86)\MySQL\MySQL Server *\bin\mysql.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($item) { return $item.FullName }

    Write-Log 'WARNING: mysql.exe not found in standard locations.'
    return $null
}

# ---------------------------------------------------------------------------
# Helper: Find-MyIniPath
# Returns the full path of my.ini in standard MySQL/MariaDB locations, or $null.
# ---------------------------------------------------------------------------

function Find-MyIniPath {
    foreach ($pattern in @(
        'C:\MySQL\*\my.ini',
        'C:\MySQL\my.ini',
        'C:\ProgramData\MySQL\MySQL Server *\my.ini',
        'C:\Program Files\MySQL\MySQL Server *\my.ini',
        'C:\Program Files\MariaDB*\data\my.ini'
    )) {
        $found = Get-Item $pattern -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($found) { return $found.FullName }
    }
    return $null
}

# ---------------------------------------------------------------------------
# Helper: Check-DbVersion
# Reads MySQL version from DB_Version.txt (written by Releem Agent).
# Returns 0 if version >= 5.6.8, 1 if version is lower.
# Exits with code 1 if the file is missing or empty.
# ---------------------------------------------------------------------------

function Check-DbVersion {
    if (Test-Path $DbVersionFilePath) {
        $db_version = (Get-Content $DbVersionFilePath -Raw).Trim()
    } else {
        Write-Host "`n * Please try again later or run Releem Agent manually:" -ForegroundColor Gray
        Write-Host "  $AgentBinaryPath -f`n" -ForegroundColor Green
        exit 1
    }

    if ([string]::IsNullOrEmpty($db_version)) {
        Write-Host "`n * Please try again later or run Releem Agent manually:" -ForegroundColor Gray
        Write-Host "  $AgentBinaryPath -f`n" -ForegroundColor Green
        exit 1
    }

    $requiredver = [Version]'5.6.8'
    if ([Version]$db_version -ge $requiredver) {
        return 0
    } else {
        return 1
    }
}

# ---------------------------------------------------------------------------
# Helper: Restart-MySqlService
# Stops and starts the MySQL/MariaDB service with polling.
# Reads the service name from mysql_restart_service in releem.conf.
# Return codes:
#   0 - service Running
#   4 - no service found / not configured
#   6 - timeout (service not Running after 1200 seconds)
#   7 - service entered Stopped status during startup polling
# ---------------------------------------------------------------------------

function Restart-MySqlService {
    $restartCmd = $releemConfig['mysql_restart_service']
    $serviceName = $null
    if ($restartCmd -and $restartCmd -match 'net stop\s+(\S+)') {
        $serviceName = $Matches[1]
    }
    if (-not $serviceName) {
        Write-Log 'ERROR: Cannot restart MySQL - mysql_restart_service not set in releem.conf.'
        return 4
    }

    Write-Log "Stopping MySQL service '$serviceName'..."
    Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue

    # Wait up to 60 seconds for the service to reach Stopped status
    $stopWait = 0
    while ($stopWait -lt 60) {
        if ((Get-Service -Name $serviceName).Status -eq 'Stopped') { break }
        Start-Sleep -Seconds 1
        $stopWait++
    }

    Write-Log "Starting MySQL service '$serviceName'..."
    Start-Service -Name $serviceName -ErrorAction SilentlyContinue

    # Poll for up to 1200 seconds; log progress every 30 seconds
    $elapsed = 0
    while ($elapsed -lt 1200) {
        Start-Sleep -Seconds 5
        $elapsed += 5
        $status = (Get-Service -Name $serviceName).Status
        if ($status -eq 'Running') {
            Write-Log "MySQL service '$serviceName' started successfully."
            return 0
        }
        if ($status -eq 'Stopped') {
            Write-Log "ERROR: MySQL service '$serviceName' entered Stopped status during startup polling."
            return 7
        }
        if ($elapsed % 30 -eq 0) {
            Write-Log "Waiting for MySQL to start... ${elapsed}s elapsed"
        }
    }

    Write-Log "ERROR: MySQL service '$serviceName' did not start within 1200 seconds (timeout)."
    return 6
}

# ---------------------------------------------------------------------------
# Helper: Invoke-ApplyConfig
# Shared logic for local apply modes.
# $Interactive=$true     → prompt user before restarting MySQL
# $RestartService=$false → copy config without restarting MySQL
# Sets $script:ExitCode and returns on error/cancellation.
# ---------------------------------------------------------------------------

function Invoke-ApplyConfig {
    param(
        [bool]$Interactive,
        [bool]$RestartService
    )

    # Check that recommended config file exists in staging area
    if (-not (Test-Path $StagingCnfPath)) {
        Write-Log "ERROR: Recommended config file not found: $StagingCnfPath"
        $script:ExitCode = 1; return
    }

    # Validate mysql_cnf_dir is configured
    if (-not $mysql_cnf_dir) {
        Write-Log 'ERROR: mysql_cnf_dir not set in releem.conf. Run -Configure first.'
        $script:ExitCode = 1; return
    }
    $liveCnfPath = Join-Path $mysql_cnf_dir $DbConfigFileName

    Write-Log 'Getting the latest up-to-date configuration.'
    if (Test-Path $AgentBinaryPath) {
        & $AgentBinaryPath -c
        $getConfigExitCode = $LASTEXITCODE
        Write-Log "releem-agent.exe -c exited with code: $getConfigExitCode"
    } else {
        Write-Log "WARNING: Releem Agent binary not found, skipping configuration refresh: $AgentBinaryPath"
    }

    # Log full contents of staging config so user can see what will be applied
    $recommendedContent = Get-Content -Path $StagingCnfPath -Raw
    Write-Log "Contents of ${StagingCnfPath}:`n$recommendedContent"

    # Check MySQL version >= 5.6.8
    Write-Log 'Checking MySQL version...'
    if ((Check-DbVersion) -ne 0) {
        Write-Log 'ERROR: MySQL version is lower than 5.6.8. Check the documentation https://github.com/Releem/mysqlconfigurer#how-to-apply-the-recommended-configuration for applying the configuration.'
        $script:ExitCode = 2; return
    }

    # Prompt for confirmation (interactive) or proceed automatically
    if ($Interactive) {
        $answer = Read-Host 'Restart MySQL service now to apply changes? [Y/N]'
        if ($answer -ne 'Y' -and $answer -ne 'y') {
            Write-Log 'User cancelled MySQL service restart. Configuration not applied.'
            $script:ExitCode = 5; return
        }
    } elseif ($RestartService) {
        Write-Log 'Running in non-interactive mode - restarting MySQL without confirmation'
    } else {
        Write-Log 'Running in non-interactive mode - copying configuration without restarting MySQL'
    }

    # Back up the current live config before applying
    $noBomUtf8 = New-Object System.Text.UTF8Encoding $false
    if (Test-Path $liveCnfPath) {
        Copy-Item -Path $liveCnfPath -Destination $BackupCnfPath -Force
        Write-Log "Backed up current live config to: $BackupCnfPath"
    } else {
        [System.IO.File]::WriteAllText($BackupCnfPath, "[mysqld]`r`n", $noBomUtf8)
        Write-Log "Created baseline backup (no prior live config): $BackupCnfPath"
    }

    # Copy staging config to MySQL conf dir
    New-Item -Path $mysql_cnf_dir -ItemType Directory -Force | Out-Null
    $stagingContent = Get-Content -Path $StagingCnfPath -Raw
    [System.IO.File]::WriteAllText($liveCnfPath, $stagingContent, $noBomUtf8)
    Write-Log "Copied recommended config to $liveCnfPath"

    if (-not $RestartService) {
        & $AgentBinaryPath --event=config_applied
        $applyEventCode = $LASTEXITCODE
        Write-Log "Agent event 'config_applied' fired with exit code $applyEventCode"
        Write-Log 'Configuration copied successfully without restart.'
        return
    }

    # Restart MySQL service
    $restartCode = Restart-MySqlService
    if ($restartCode -eq 6) {
        Write-Log 'ERROR: MySQL service failed to restart in 1200 seconds. Wait for the MySQL service to start and check the MySQL error log.'
        Write-Log 'To roll back the configuration, run: .\mysqlconfigurer.ps1 -r'
        $script:ExitCode = $restartCode; return
    }
    if ($restartCode -eq 7) {
        Write-Log 'ERROR: MySQL service failed to restart. Check the MySQL error log.'
        Write-Log 'To roll back the configuration, run: .\mysqlconfigurer.ps1 -r'
        $script:ExitCode = $restartCode; return
    }
    if ($restartCode -ne 0) {
        Write-Log "ERROR: MySQL service restart failed with code $restartCode."
        $script:ExitCode = $restartCode; return
    }

    # Successfully applied — remove backup
    Remove-Item -Path $BackupCnfPath -Force
    Write-Log "Backup removed after successful apply: $BackupCnfPath"

    # Fire config_applied event (non-fatal)
    & $AgentBinaryPath --event=config_applied
    $applyEventCode = $LASTEXITCODE
    Write-Log "Agent event 'config_applied' fired with exit code $applyEventCode"

    Write-Log 'Configuration applied successfully.'
}

# ---------------------------------------------------------------------------
# Admin privilege check
# ---------------------------------------------------------------------------

$currentPrincipal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host 'ERROR: This script must be run as Administrator.'
    exit 1
}

# ---------------------------------------------------------------------------
# Logging setup: ensure log directory exists
# ---------------------------------------------------------------------------

New-Item -Path 'C:\ProgramData\ReleemAgent' -ItemType Directory -Force | Out-Null
$timestamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
Add-Content -Path $LogFilePath -Value "=== Releem MySQL Configurer $ScriptVersion  $timestamp ==="

# ---------------------------------------------------------------------------
# Exit code tracker
# ---------------------------------------------------------------------------

$script:ExitCode = 0

# ---------------------------------------------------------------------------
# Read config and resolve API key
# ---------------------------------------------------------------------------

$releemConfig = Read-ReleemConfig

$apikey         = if ($releemConfig.ContainsKey('apikey'))         { $releemConfig['apikey'] }         else { '' }
$mysql_host     = if ($releemConfig.ContainsKey('mysql_host'))     { $releemConfig['mysql_host'] }     else { '127.0.0.1' }
$mysql_port     = if ($releemConfig.ContainsKey('mysql_port'))     { $releemConfig['mysql_port'] }     else { '3306' }
$mysql_user     = if ($releemConfig.ContainsKey('mysql_user'))     { $releemConfig['mysql_user'] }     else { '' }
$mysql_password = if ($releemConfig.ContainsKey('mysql_password')) { $releemConfig['mysql_password'] } else { '' }
$mysql_cnf_dir  = if ($releemConfig.ContainsKey('mysql_cnf_dir'))  { $releemConfig['mysql_cnf_dir'] }  else { '' }

# -ApiKey parameter overrides releem.conf value
if ($ApiKey) {
    $apikey = $ApiKey
}

# Determine API base URL
$ApiBaseUrl = if ($env:RELEEM_REGION -eq 'EU') { 'https://api.eu.releem.com' } else { 'https://api.releem.com' }

# ---------------------------------------------------------------------------
# Main dispatch
# ---------------------------------------------------------------------------

try {

    if ($NonInteractive -and -not $Apply) {
        Write-Log 'ERROR: -NonInteractive can only be used together with -Apply.'
        $script:ExitCode = 1; return
    }
    if ($NoRestart -and -not ($Apply -and $NonInteractive)) {
        Write-Log 'ERROR: -NoRestart can only be used together with -Apply -NonInteractive.'
        $script:ExitCode = 1; return
    }

    if (-not ($Apply -or $QueueApply -or $Rollback -or $Configure -or $Update)) {
        Write-Host ''
        Write-Host 'Usage: mysqlconfigurer.ps1 [flags] [-ApiKey <key>]'
        Write-Host ''
        Write-Host 'Flags:'
        Write-Host '  -Configure   Enable Performance Schema and slow query log in MySQL'
        Write-Host '  -Apply       Apply Releem recommended MySQL config locally'
        Write-Host '  -NonInteractive   Use with -Apply to skip the restart confirmation prompt'
        Write-Host '  -NoRestart   Use with -Apply -NonInteractive to copy config without restarting MySQL'
        Write-Host '  -QueueApply  Ask Releem Agent to create an apply_config job'
        Write-Host '  -Rollback    Restore previous MySQL configuration from backup'
        Write-Host '  -Update      Refresh install.ps1 and delegate the full agent update flow'
        Write-Host ''
        $script:ExitCode = 0; return
    }

    if ($Configure) {
        Write-Log 'Starting -Configure: enabling Performance Schema and slow query log...'

        # Check MySQL version >= 5.6.8
        Write-Log 'Checking MySQL version...'
        if ((Check-DbVersion) -ne 0) {
            Write-Log 'ERROR: MySQL version is lower than 5.6.8. Check the documentation https://github.com/Releem/mysqlconfigurer#how-to-apply-the-recommended-configuration for applying the configuration.'
            $script:ExitCode = 2; return
        }

        # Find my.ini and derive MySQL conf dir (releem.conf.d alongside my.ini)
        $myIniPath = Find-MyIniPath
        if (-not $myIniPath) {
            Write-Log 'ERROR: my.ini not found. Cannot determine MySQL configuration directory.'
            $script:ExitCode = 1; return
        }
        Write-Log "Found MySQL config file: $myIniPath"
        $mysqlConfDir = Join-Path (Split-Path $myIniPath -Parent) 'releem.conf.d'

        # Ensure MySQL conf dir exists
        New-Item -Path $mysqlConfDir -ItemType Directory -Force | Out-Null
        Write-Log "MySQL conf dir ready: $mysqlConfDir"

        # Write releem.cnf with Performance Schema and slow query log settings (no BOM)
        $ReleemCnfPath = Join-Path $mysqlConfDir 'releem.cnf'
        $releemCnfContent = "[mysqld]`r`nperformance_schema=1`r`nslow_query_log=1`r`nperformance-schema-consumer-events-statements-history=ON`r`nperformance-schema-consumer-events-statements-current=ON"
        $noBomUtf8 = New-Object System.Text.UTF8Encoding $false
        [System.IO.File]::WriteAllText($ReleemCnfPath, $releemCnfContent, $noBomUtf8)
        Write-Log "Created MySQL performance_schema config: $ReleemCnfPath"

        # Add !includedir to my.ini if not already present (append-only to preserve original encoding)
        $myIniContent = Get-Content -Path $myIniPath -Raw
        $includeDir = "!includedir $mysqlConfDir"
        if ($myIniContent -notmatch [regex]::Escape($includeDir)) {
            [System.IO.File]::AppendAllText($myIniPath, "`r`n$includeDir`r`n")
            Write-Log "Added '$includeDir' to $myIniPath"
        } else {
            Write-Log "MySQL config already contains '$includeDir'. No changes made."
        }

        # Persist mysql_cnf_dir to releem.conf and refresh in-memory value
        Set-ReleemConfigValue -Key 'mysql_cnf_dir' -Value ($mysqlConfDir -replace '\\', '\\')
        Write-Log "Updated releem.conf: mysql_cnf_dir=$mysqlConfDir"
        $mysql_cnf_dir = $mysqlConfDir

        Write-Log '-Configure completed successfully.'
    }

    if ($Apply) {
        if ($NonInteractive -and $NoRestart) {
            Write-Log 'Starting -Apply -NonInteractive -NoRestart: copying recommended MySQL configuration without restarting MySQL...'
        } elseif ($NonInteractive) {
            Write-Log 'Starting -Apply -NonInteractive: applying recommended MySQL configuration non-interactively...'
        } else {
            Write-Log 'Starting -Apply: applying recommended MySQL configuration...'
        }
        Invoke-ApplyConfig -Interactive (-not $NonInteractive) -RestartService (-not $NoRestart)
    }

    if ($QueueApply) {
        Write-Log 'Starting -QueueApply: sending request to create an apply_config job...'
        if (-not (Test-Path $AgentBinaryPath)) {
            Write-Log "ERROR: Releem Agent binary not found: $AgentBinaryPath"
            $script:ExitCode = 1; return
        }

        & $AgentBinaryPath --task=apply_config
        $queueExitCode = $LASTEXITCODE
        if ($queueExitCode -ne 0) {
            Write-Log "ERROR: Failed to queue apply_config task. Releem Agent exited with code $queueExitCode."
            $script:ExitCode = $queueExitCode; return
        }

        Write-Log 'Sending request to create a job to apply the configuration.'
    }

    if ($Rollback) {
        Write-Log 'Starting -Rollback: restoring previous MySQL configuration...'

        # Validate mysql_cnf_dir
        if (-not $mysql_cnf_dir) {
            Write-Log 'ERROR: mysql_cnf_dir not set in releem.conf. Run -Configure first.'
            $script:ExitCode = 1; return
        }
        $liveCnfPath = Join-Path $mysql_cnf_dir $DbConfigFileName

        # Prompt user for confirmation (or allow non-interactive confirm via env var)
        if ($env:RELEEM_ROLLBACK_CONFIRM -eq '1') {
            $answer = 'Y'
            Write-Log 'Rollback confirmation accepted via RELEEM_ROLLBACK_CONFIRM=1'
        } else {
            $answer = Read-Host 'Restart MySQL service to rollback configuration? [Y/N]'
        }
        if ($answer -ne 'Y' -and $answer -ne 'y') {
            Write-Log 'User cancelled MySQL service restart. Rollback not performed.'
            $script:ExitCode = 5; return
        }

        if (Test-Path $BackupCnfPath) {
            # Restore backup to live location
            Write-Log "Restoring backup $BackupCnfPath to $liveCnfPath"
            Copy-Item -Path $BackupCnfPath -Destination $liveCnfPath -Force
        } else {
            # No backup — delete live config so MySQL starts without Releem settings
            Write-Log "No backup found. Deleting live config: $liveCnfPath"
            if (Test-Path $liveCnfPath) {
                Remove-Item -Path $liveCnfPath -Force
            }
        }

        # Restart MySQL service
        $restartCode = Restart-MySqlService
        if ($restartCode -eq 6) {
            Write-Log 'ERROR: MySQL service failed to restart in 1200 seconds. Wait for the MySQL service to start and check the MySQL error log.'
            $script:ExitCode = $restartCode; return
        }
        if ($restartCode -eq 7) {
            Write-Log 'ERROR: MySQL service failed to restart. Check the MySQL error log.'
            $script:ExitCode = $restartCode; return
        }
        if ($restartCode -ne 0) {
            Write-Log "ERROR: MySQL service restart failed with code $restartCode."
            $script:ExitCode = $restartCode; return
        }

        # Remove backup on successful restart
        if (Test-Path $BackupCnfPath) {
            Remove-Item -Path $BackupCnfPath -Force
            Write-Log "Backup file removed: $BackupCnfPath"
        }

        # Fire config_rollback event (non-fatal)
        & $AgentBinaryPath --event=config_rollback
        $rollbackEventCode = $LASTEXITCODE
        Write-Log "Agent event 'config_rollback' fired with exit code $rollbackEventCode"

        Write-Log 'Rollback completed successfully.'
    }

    if ($Update) {
        Write-Log 'Starting -Update: checking for latest Releem Agent version...'

        $tempPath = Join-Path $env:TEMP 'install_new.ps1'

        # Fetch remote version
        $remoteVersionRaw = $null
        try {
            $remoteVersionRaw = (Invoke-WebRequest -Uri $CurrentVersionUrl -UseBasicParsing -ErrorAction Stop).Content.Trim()
        } catch {
            Write-Log "ERROR: Failed to fetch remote version from ${CurrentVersionUrl}: $_"
            $script:ExitCode = 1; return
        }

        Write-Log "Remote version: $remoteVersionRaw  Current version: $ScriptVersion"

        # Semantic version comparison: compare element by element
        $currentParts = $ScriptVersion.Trim().Split('.') | ForEach-Object { [int]$_ }
        $remoteParts  = $remoteVersionRaw.Split('.')     | ForEach-Object { [int]$_ }

        $isNewer = $false
        $partsToCompare = [Math]::Max($currentParts.Count, $remoteParts.Count)
        for ($i = 0; $i -lt $partsToCompare; $i++) {
            $c = if ($i -lt $currentParts.Count) { $currentParts[$i] } else { 0 }
            $r = if ($i -lt $remoteParts.Count)  { $remoteParts[$i]  } else { 0 }
            if ($r -gt $c) { $isNewer = $true; break }
            if ($r -lt $c) { break }
        }

        if (-not $isNewer) {
            Write-Log "Releem Agent is up to date (version $ScriptVersion)"
            $script:ExitCode = 0; return
        }

        Write-Log "Newer version $remoteVersionRaw available. Refreshing installer script..."

        # Download the latest installer script, then delegate update work to it.
        try {
            Invoke-WebRequest -Uri $InstallerScriptUrl -OutFile $tempPath -UseBasicParsing -ErrorAction Stop
        } catch {
            Write-Log "ERROR: Failed to download installer script from ${InstallerScriptUrl}: $_"
            $script:ExitCode = 1; return
        }

        try {
            New-Item -Path ([System.IO.Path]::GetDirectoryName($InstallerScriptPath)) -ItemType Directory -Force | Out-Null
            Copy-Item -Path $tempPath -Destination $InstallerScriptPath -Force -ErrorAction Stop
        } catch {
            Write-Log "ERROR: Failed to copy installer script to ${InstallerScriptPath}: $_"
            $script:ExitCode = 1; return
        }

        Write-Log "Installer script refreshed at $InstallerScriptPath"
        Write-Log "Delegating update to $InstallerScriptPath -u"

        $hadApiKey = Test-Path Env:RELEEM_API_KEY
        $previousApiKey = $env:RELEEM_API_KEY
        if ($apikey) {
            $env:RELEEM_API_KEY = $apikey
        }

        try {
            & powershell.exe -ExecutionPolicy Bypass -File $InstallerScriptPath -u
            $installExitCode = $LASTEXITCODE
        } finally {
            if ($hadApiKey) {
                $env:RELEEM_API_KEY = $previousApiKey
            } else {
                Remove-Item Env:RELEEM_API_KEY -ErrorAction SilentlyContinue
            }
        }

        if ($installExitCode -ne 0) {
            Write-Log "ERROR: install.ps1 -u failed with exit code $installExitCode."
            $script:ExitCode = $installExitCode
            return
        }

        Write-Log '-Update completed successfully.'
        $script:ExitCode = 0
        return
    }

} catch {
    Write-Log "ERROR: An unexpected error occurred: $_"
    $script:ExitCode = 1
} finally {
    Send-ConfigurerLog
    exit $script:ExitCode
}
