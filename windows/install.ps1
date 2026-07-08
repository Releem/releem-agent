<#
.SYNOPSIS
    Releem Agent installer for Windows.

.DESCRIPTION
    Installs, updates, or uninstalls the Releem Agent on Windows.
    Can be deployed silently via environment variables or interactively.

.PARAMETER u
    Run in update mode: replace the agent binary and restart the service.

.PARAMETER Uninstall
    Run in uninstall mode: stop and remove the agent, service, and scheduled task.

.EXAMPLE
    # Install (default)
    .\install.ps1

    # Update
    .\install.ps1 -u

    # Uninstall
    .\install.ps1 -Uninstall
#>

[CmdletBinding()]
param(
    [switch]$u,
    [switch]$Uninstall
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

$AgentBinaryPath         = 'C:\Program Files\ReleemAgent\releem-agent.exe'
$InstallerScriptPath     = 'C:\Program Files\ReleemAgent\install.ps1'
$ConfigurerScriptPath    = 'C:\Program Files\ReleemAgent\mysqlconfigurer.ps1'
$ConfigFilePath          = 'C:\ProgramData\ReleemAgent\releem.conf'
$ConfDirPath             = 'C:\ProgramData\ReleemAgent\conf.d'
$InstallLogPath          = 'C:\ProgramData\ReleemAgent\releem-install.log'
$AgentDownloadUrl        = 'https://releem.s3.us-east-1.amazonaws.com/v2/releem-agent.exe'
$InstallerUrl            = 'https://releem.s3.us-east-1.amazonaws.com/v2/install.ps1'
$ReleemAgentScriptUrl    = 'https://releem.s3.us-east-1.amazonaws.com/v2/mysqlconfigurer.ps1'
$ApiUrlUS                = 'https://api.releem.com'
$ApiUrlEU                = 'https://api.eu.releem.com'

# ---------------------------------------------------------------------------
# Helper: Write-Log
# Writes a message to both the console and the install log file.
# ---------------------------------------------------------------------------

function Write-Log {
    param([string]$Message)
    Write-Host $Message
    Add-Content -Path $InstallLogPath -Value $Message
}

# ---------------------------------------------------------------------------
# Helper: Find-MyIniPath
# Searches standard locations for my.ini; returns full path or $null.
# ---------------------------------------------------------------------------

function Find-MyIniPath {
    foreach ($pattern in @(
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
# Helper: Invoke-MySQL
# Runs mysql.exe suppressing stderr warnings without triggering
# $ErrorActionPreference = 'Stop' on native command stderr output.
# Returns captured stdout; sets $LASTEXITCODE as usual.
# ---------------------------------------------------------------------------

function Invoke-MySQL {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = & $MysqlExe @args 2>$null
    $ErrorActionPreference = $prev
    return $output
}

function Get-MySQLRootArgs {
    $rootLogin = if ($env:RELEEM_MYSQL_ROOT_LOGIN) { $env:RELEEM_MYSQL_ROOT_LOGIN } else { 'root' }
    $rootArgs = @('-u', $rootLogin)
    if ([System.Environment]::GetEnvironmentVariable('RELEEM_MYSQL_ROOT_PASSWORD') -ne $null) {
        $rootArgs += "-p$env:RELEEM_MYSQL_ROOT_PASSWORD"
    }
    return $rootArgs
}

function Invoke-MySQLRoot {
    $mysqlArgs = @('-h', $MysqlHost, '-P', $MysqlPort) + (Get-MySQLRootArgs) + $args
    return Invoke-MySQL @mysqlArgs
}

function Test-MySQLRootConnection {
    $null = Invoke-MySQLRoot -e 'SELECT 1;'
    return ($LASTEXITCODE -eq 0)
}

function Convert-SecureStringToPlainText {
    param([System.Security.SecureString]$SecureString)

    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($SecureString)
    try {
        return [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    } finally {
        if ($bstr -ne [IntPtr]::Zero) {
            [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
        }
    }
}

function Prompt-MySQLRootPasswordUntilSuccess {
    while ($true) {
        if ([Console]::IsInputRedirected) {
            Write-Host 'Please enter MySQL root password'
            $env:RELEEM_MYSQL_ROOT_PASSWORD = [Console]::In.ReadLine()
        } else {
            $securePassword = Read-Host -Prompt 'Please enter MySQL root password' -AsSecureString
            $env:RELEEM_MYSQL_ROOT_PASSWORD = Convert-SecureStringToPlainText $securePassword
        }

        if (Test-MySQLRootConnection) {
            return $true
        }

        Write-Log 'ERROR: MySQL connection failed with the entered root password.'
    }
}

# ---------------------------------------------------------------------------
# Helper: Stop-ReleemAgentForReplacement
# Stops the running service and waits for the binary to be released so the
# installer can safely overwrite releem-agent.exe on reinstall/update.
# ---------------------------------------------------------------------------

function Stop-ReleemAgentForReplacement {
    param(
        [string]$Reason = 'before replacing releem-agent.exe'
    )

    $svc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
    if (-not $svc) {
        Write-Log "No existing Releem Agent service found; skipping stop $Reason."
        return
    }

    Write-Log "Stopping Releem Agent service $Reason..."

    if (Test-Path $AgentBinaryPath) {
        & $AgentBinaryPath stop
        Write-Log "releem-agent.exe stop exited with code: $LASTEXITCODE"
    }

    $svc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -eq 'Running') {
        Stop-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
    }

    $waited = 0
    while ($waited -lt 30) {
        $svc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
        if (-not $svc -or $svc.Status -ne 'Running') {
            Write-Log 'Releem Agent service is stopped.'
            return
        }
        Start-Sleep -Seconds 1
        $waited++
    }

    throw "Releem Agent service did not stop within 30 seconds $Reason."
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
# Logging setup: create log directory and write timestamped run header
# ---------------------------------------------------------------------------

New-Item -Path 'C:\ProgramData\ReleemAgent' -ItemType Directory -Force | Out-Null
$timestamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
Add-Content -Path $InstallLogPath -Value "=== Releem Install $timestamp ==="

# ---------------------------------------------------------------------------
# Exit code tracker and log-upload helper
# ---------------------------------------------------------------------------

$script:MainExitCode = 0

function Send-InstallLog {
    $apiKeyVar = Get-Variable -Name 'ApiKey' -Scope Script -ErrorAction SilentlyContinue
    $apiKey = if ($apiKeyVar) { $apiKeyVar.Value } else { '' }

    if (-not $apiKey) {
        Write-Host 'INFO: No API key available; skipping install log upload.'
        return
    }

    $apiBase = if ($env:RELEEM_REGION -eq 'EU') { $ApiUrlEU } else { $ApiUrlUS }
    $logUrl = "$apiBase/v2/events/saving_log"

    try {
        $logContent = if (Test-Path $InstallLogPath) { Get-Content -Path $InstallLogPath -Raw } else { '' }
        $headers = @{ 'x-releem-api-key' = $apiKey }
        Invoke-RestMethod -Uri $logUrl -Method Post -Body $logContent -Headers $headers -ErrorAction Stop | Out-Null
        Write-Host 'Install log uploaded to Releem API.'
    } catch {
        $warnMsg = "WARNING: Failed to upload install log to Releem API: $_"
        try { Write-Log $warnMsg } catch { Write-Host $warnMsg }
    }
}

# ---------------------------------------------------------------------------
# Mode dispatch
# ---------------------------------------------------------------------------

try {

if ($u) {
    Write-Log '=== Update mode ==='

    # ---------------------------------------------------------------------------
    # Stop the service
    # ---------------------------------------------------------------------------
    Stop-ReleemAgentForReplacement -Reason 'before update'

    # ---------------------------------------------------------------------------
    # Download the latest releem-agent.exe binary
    # ---------------------------------------------------------------------------
    Write-Log "Downloading latest Releem Agent binary from $AgentDownloadUrl ..."
    try {
        Invoke-WebRequest -Uri $AgentDownloadUrl -OutFile $AgentBinaryPath -UseBasicParsing
    } catch {
        Write-Log "ERROR: Failed to download Releem Agent binary. $_"
        $script:MainExitCode = 1; return
    }

    if (-not (Test-Path $AgentBinaryPath)) {
        Write-Log 'ERROR: Releem Agent binary not found after download.'
        $script:MainExitCode = 1; return
    }
    $binarySize = (Get-Item $AgentBinaryPath).Length
    if ($binarySize -le 0) {
        Write-Log 'ERROR: Downloaded Releem Agent binary is empty.'
        $script:MainExitCode = 1; return
    }
    Write-Log "Releem Agent binary updated ($binarySize bytes): $AgentBinaryPath"

    # ---------------------------------------------------------------------------
    # Download the latest install.ps1 and mysqlconfigurer.ps1
    # ---------------------------------------------------------------------------
    Write-Log "Downloading latest installer script from $InstallerUrl ..."
    try {
        Invoke-WebRequest -Uri $InstallerUrl -OutFile $InstallerScriptPath -UseBasicParsing
    } catch {
        Write-Log "ERROR: Failed to download installer script. $_"
        $script:MainExitCode = 1; return
    }
    Write-Log "Installer script refreshed: $InstallerScriptPath"

    Write-Log "Downloading latest mysqlconfigurer script from $ReleemAgentScriptUrl ..."
    try {
        Invoke-WebRequest -Uri $ReleemAgentScriptUrl -OutFile $ConfigurerScriptPath -UseBasicParsing
    } catch {
        Write-Log "WARNING: Failed to download mysqlconfigurer script. $_"
    }
    Write-Log "Mysqlconfigurer script refreshed: $ConfigurerScriptPath"

    # ---------------------------------------------------------------------------
    # Start the service
    # ---------------------------------------------------------------------------
    Write-Log 'Starting Releem Agent service...'
    & $AgentBinaryPath start
    if ($LASTEXITCODE -ne 0) {
        Write-Log "ERROR: 'releem-agent.exe start' failed with exit code $LASTEXITCODE."
        $script:MainExitCode = 1; return
    }
    Write-Log 'Releem Agent service start command issued.'

    # Verify the service is actually running
    $svc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
    if (-not $svc -or $svc.Status -ne 'Running') {
        $svcStatus = if ($svc) { $svc.Status } else { 'not found' }
        Write-Log "ERROR: Releem Agent service is not running after update. Current status: $svcStatus"
        $script:MainExitCode = 1; return
    }
    Write-Log 'Releem Agent service is running.'

    Write-Log 'Releem Agent updated successfully.'
    $script:MainExitCode = 0; return
}

if ($Uninstall) {
    Write-Log '=== Uninstall mode ==='

    # Read API key from env var or existing releem.conf for log upload
    if ($env:RELEEM_API_KEY) {
        $ApiKey = $env:RELEEM_API_KEY
    } elseif (Test-Path $ConfigFilePath) {
        $confLine = Get-Content $ConfigFilePath | Where-Object { $_ -match '^apikey=' }
        if ($confLine) { $ApiKey = ($confLine -replace '^apikey="?([^"]*)"?$', '$1') }
    }

    # ---------------------------------------------------------------------------
    # Confirmation prompt
    # ---------------------------------------------------------------------------
    if ($env:RELEEM_UNINSTALL_CONFIRM -eq '1') {
        Write-Log 'Uninstall confirmed via RELEEM_UNINSTALL_CONFIRM=1 (silent mode).'
    } else {
        $confirmChoice = Read-Host 'This will remove the Releem Agent. Continue? [Y/n]'
        if ($confirmChoice -eq 'n' -or $confirmChoice -eq 'N') {
            Write-Log 'Uninstall aborted by user.'
            $script:MainExitCode = 0; return
        }
    }

    # ---------------------------------------------------------------------------
    # Stop and uninstall the service (non-fatal if binary missing)
    # ---------------------------------------------------------------------------
    if (Test-Path $AgentBinaryPath) {
        Write-Log 'Stopping Releem Agent service...'
        & $AgentBinaryPath stop
        Write-Log "releem-agent.exe stop exited with code: $LASTEXITCODE"

        Write-Log 'Removing Releem Agent service...'
        & $AgentBinaryPath remove
        Write-Log "releem-agent.exe remove exited with code: $LASTEXITCODE"
    } else {
        Write-Log 'WARNING: releem-agent.exe not found; skipping service stop and remove.'
    }

    # ---------------------------------------------------------------------------
    # Remove Scheduled Task
    # ---------------------------------------------------------------------------
    Unregister-ScheduledTask -TaskName 'ReleemAgentUpdate' -Confirm:$false -ErrorAction SilentlyContinue
    Write-Log 'Scheduled Task ReleemAgentUpdate removed (if it existed).'

    # ---------------------------------------------------------------------------
    # Remove agent directories
    # ---------------------------------------------------------------------------
    $agentDir = 'C:\Program Files\ReleemAgent'
    if (Test-Path $agentDir) {
        Write-Log "Removing: $agentDir"
        Remove-Item -Path $agentDir -Recurse -Force
        Write-Host "Removed: $agentDir"
    } else {
        Write-Log "Directory not found (already removed?): $agentDir"
    }

    # ---------------------------------------------------------------------------
    # Remove !includedir from my.ini
    # ---------------------------------------------------------------------------
    $myIniPath = Find-MyIniPath
    if ($myIniPath) {
        $ansi = [System.Text.Encoding]::Default
        $myIniContent = [System.IO.File]::ReadAllText($myIniPath, $ansi)
        if ($myIniContent -match '!includedir[^\r\n]*releem\.conf\.d') {
            $newContent = $myIniContent -replace '\r?\n!includedir[^\r\n]*releem\.conf\.d[^\r\n]*', ''
            [System.IO.File]::WriteAllText($myIniPath, $newContent, $ansi)
            Write-Log "Removed !includedir from $myIniPath"
        } else {
            Write-Log "!includedir not found in $myIniPath - no changes made."
        }
    } else {
        Write-Log 'WARNING: my.ini not found - could not remove !includedir automatically.'
    }

    Write-Log 'Removing C:\ProgramData\ReleemAgent (including log file)...'

    $dataDir = 'C:\ProgramData\ReleemAgent'
    if (Test-Path $dataDir) {
        # Reset ACLs on all files before removal (releem.conf has restricted permissions)
        & icacls $dataDir /reset /T /Q | Out-Null
        Remove-Item -Path $dataDir -Recurse -Force
        Write-Host "Removed: $dataDir"
    } else {
        Write-Host "Directory not found (already removed?): $dataDir"
    }

    Write-Host 'Releem Agent uninstalled successfully.'
    $script:MainExitCode = 0; return
}

# Default: Install mode
Write-Log 'Install mode'

# ---------------------------------------------------------------------------
# API key collection
# ---------------------------------------------------------------------------

if ($env:RELEEM_API_KEY) {
    $ApiKey = $env:RELEEM_API_KEY
} else {
    $ApiKey = Read-Host 'Enter your Releem API key'
}

if (-not $ApiKey) {
    Write-Log 'ERROR: Releem API key is required.'
    $script:MainExitCode = 1; return
}

# ---------------------------------------------------------------------------
# MySQL connection setup
# ---------------------------------------------------------------------------

$MysqlHost = if ($env:RELEEM_MYSQL_HOST) { $env:RELEEM_MYSQL_HOST } else { '127.0.0.1' }
$MysqlPort = if ($env:RELEEM_MYSQL_PORT) { $env:RELEEM_MYSQL_PORT } else { '3306' }

# Locate mysql.exe: search PATH first, then well-known install locations
$MysqlExe = $null
$mysqlCmd = Get-Command mysql.exe -ErrorAction SilentlyContinue
if ($mysqlCmd) {
    $MysqlExe = $mysqlCmd.Source
} else {
    foreach ($pattern in @('C:\Program Files\MySQL\*\bin\mysql.exe', 'C:\Program Files\MariaDB*\bin\mysql.exe')) {
        $found = Get-Item $pattern -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($found) { $MysqlExe = $found.FullName; break }
    }
}

if (-not $MysqlExe) {
    Write-Log 'ERROR: mysql.exe not found. Please install MySQL client tools and ensure mysql.exe is in your PATH.'
    $script:MainExitCode = 1; return
}

Write-Log "Using MySQL client: $MysqlExe"

# ---------------------------------------------------------------------------
# Required directory creation
# ---------------------------------------------------------------------------

Write-Log 'Creating required directories...'

New-Item -Path 'C:\ProgramData\ReleemAgent' -ItemType Directory -Force | Out-Null
Write-Log 'Directory ready: C:\ProgramData\ReleemAgent'

New-Item -Path 'C:\ProgramData\ReleemAgent\conf.d' -ItemType Directory -Force | Out-Null
Write-Log 'Directory ready: C:\ProgramData\ReleemAgent\conf.d'

New-Item -Path 'C:\Program Files\ReleemAgent' -ItemType Directory -Force | Out-Null
Write-Log 'Directory ready: C:\Program Files\ReleemAgent'

# ---------------------------------------------------------------------------
# Stop the existing agent before replacing the binary during reinstall.
# ---------------------------------------------------------------------------

Stop-ReleemAgentForReplacement -Reason 'before reinstall'

# ---------------------------------------------------------------------------
# Agent binary download from S3
# ---------------------------------------------------------------------------

Write-Log "Downloading Releem Agent from $AgentDownloadUrl ..."

try {
    Invoke-WebRequest -Uri $AgentDownloadUrl -OutFile $AgentBinaryPath -UseBasicParsing
} catch {
    Write-Log "ERROR: Failed to download Releem Agent binary. $_"
    $script:MainExitCode = 1; return
}

if (-not (Test-Path $AgentBinaryPath)) {
    Write-Log 'ERROR: Releem Agent binary not found after download.'
    $script:MainExitCode = 1; return
}

$binarySize = (Get-Item $AgentBinaryPath).Length
if ($binarySize -le 0) {
    Write-Log 'ERROR: Downloaded Releem Agent binary is empty.'
    $script:MainExitCode = 1; return
}

Write-Log "Releem Agent binary downloaded successfully ($binarySize bytes): $AgentBinaryPath"

# ---------------------------------------------------------------------------
# MySQL user setup (create_mysql_user)
# ---------------------------------------------------------------------------

$InstanceType        = if ($env:RELEEM_INSTANCE_TYPE) { $env:RELEEM_INSTANCE_TYPE } else { 'local' }
$ReleemMysqlLogin    = ''
$ReleemMysqlPassword = ''
$at                  = '@'
$MysqlUserHost       = if ($MysqlHost -eq '127.0.0.1' -or $MysqlHost -eq 'localhost') { '127.0.0.1' } else { '%' }

Write-Log 'Configuring the MySQL user for metrics collection.'

$FLAG_SUCCESS = 0

if ($env:RELEEM_MYSQL_PASSWORD -and $env:RELEEM_MYSQL_LOGIN) {
    Write-Log 'Using MySQL login and password from environment variables.'
    $ReleemMysqlLogin    = $env:RELEEM_MYSQL_LOGIN
    $ReleemMysqlPassword = $env:RELEEM_MYSQL_PASSWORD
    $FLAG_SUCCESS = 1

} else {
    Write-Log 'Using MySQL root user.'

    $rootConnectionSuccessful = Test-MySQLRootConnection
    if (-not $rootConnectionSuccessful -and [System.Environment]::GetEnvironmentVariable('RELEEM_MYSQL_ROOT_PASSWORD') -eq $null) {
        $rootConnectionSuccessful = Prompt-MySQLRootPasswordUntilSuccess
    }

    if ($rootConnectionSuccessful) {
        Write-Log 'MySQL connection successful.'

        $ReleemMysqlLogin    = 'releem'
        $charSet             = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#%'.ToCharArray()
        $ReleemMysqlPassword = -join (1..16 | ForEach-Object { $charSet | Get-Random })

        $null = Invoke-MySQLRoot -e "DROP USER '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "CREATE USER '$ReleemMysqlLogin'$at'$MysqlUserHost' IDENTIFIED BY '$ReleemMysqlPassword';"
        $null = Invoke-MySQLRoot -e "GRANT PROCESS ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "GRANT REPLICATION CLIENT ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "GRANT SHOW VIEW ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "GRANT SELECT ON mysql.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"

        # Non-fatal performance_schema grants
        $null = Invoke-MySQLRoot -e "GRANT SELECT ON performance_schema.events_statements_summary_by_digest TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "GRANT SELECT ON performance_schema.table_io_waits_summary_by_index_usage TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        $null = Invoke-MySQLRoot -e "GRANT SELECT ON performance_schema.file_summary_by_instance TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"

        # SYSTEM_VARIABLES_ADMIN or SUPER (non-fatal)
        $null = Invoke-MySQLRoot -e "GRANT SYSTEM_VARIABLES_ADMIN ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        if ($LASTEXITCODE -ne 0) {
            $null = Invoke-MySQLRoot -e "GRANT SUPER ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        }

        if ($env:RELEEM_QUERY_OPTIMIZATION) {
            $null = Invoke-MySQLRoot -e "GRANT SELECT ON *.* TO '$ReleemMysqlLogin'$at'$MysqlUserHost';"
        }

        Write-Log "Created new user '$ReleemMysqlLogin'."
        $FLAG_SUCCESS = 1

    } else {
        $rootLogin = if ($env:RELEEM_MYSQL_ROOT_LOGIN) { $env:RELEEM_MYSQL_ROOT_LOGIN } else { 'root' }
        Write-Log "ERROR: MySQL connection failed with user $rootLogin. Check that RELEEM_MYSQL_ROOT_PASSWORD is correct and run reinstall the agent."
        $script:MainExitCode = 1; return
    }
}

if ($FLAG_SUCCESS -eq 1) {
    $null = Invoke-MySQL -h $MysqlHost -P $MysqlPort -u $ReleemMysqlLogin "-p$ReleemMysqlPassword" -e 'SELECT 1;'
    if ($LASTEXITCODE -eq 0) {
        Write-Log "MySQL connection with user '$ReleemMysqlLogin' - successful."
    } else {
        Write-Log "ERROR: MySQL connection failed with user '$ReleemMysqlLogin'. Check that the user and password is correct and reinstall the agent."
        $script:MainExitCode = 1; return
    }
}

# ---------------------------------------------------------------------------
# MySQL conf dir detection (used in releem.conf; -Configure writes the files)
# ---------------------------------------------------------------------------

$MysqlConfDir = ''

if ($InstanceType -ne 'aws/rds' -and $InstanceType -ne 'gcp/cloudsql') {
    $myIniPath = Find-MyIniPath
    if ($myIniPath) {
        Write-Log "Found MySQL config file: $myIniPath"
        $MysqlConfDir = Join-Path (Split-Path $myIniPath -Parent) 'releem.conf.d'
    } else {
        Write-Log 'WARNING: MySQL config file (my.ini) not found in common locations.'
    }
}

# ---------------------------------------------------------------------------
# MySQL service detection
# ---------------------------------------------------------------------------

$MysqlServiceName = $null
if ($InstanceType -ne 'aws/rds' -and $InstanceType -ne 'gcp/cloudsql') {
    foreach ($name in @('MySQL84', 'MySQL80', 'MySQL57', 'MySQL56', 'MySQL', 'mariadb', 'mysqld')) {
        $svc = Get-Service -Name $name -ErrorAction SilentlyContinue
        if ($svc) {
            $MysqlServiceName = $name
            Write-Log "Detected MySQL service: $MysqlServiceName"
            break
        }
    }
    if (-not $MysqlServiceName) {
        Write-Log 'WARNING: Could not detect MySQL service name. mysql_restart_service will not be set in releem.conf.'
    }
}

# ---------------------------------------------------------------------------
# releem.conf generation with restricted ACL
# ---------------------------------------------------------------------------

$restartCmd = if ($MysqlServiceName) { "net stop $MysqlServiceName && net start $MysqlServiceName" } else { '' }
$confDirEscaped = $ConfDirPath -replace '\\', '\\'
$confLines = @(
    "apikey=`"$ApiKey`"",
    "releem_cnf_dir=`"$confDirEscaped`"",
    "mysql_host=`"$MysqlHost`"",
    "mysql_port=`"$MysqlPort`"",
    "mysql_user=`"$ReleemMysqlLogin`"",
    "mysql_password=`"$ReleemMysqlPassword`"",
    "mysql_restart_service=`"$restartCmd`"",
    "mysql_cnf_dir=`"$($MysqlConfDir -replace '\\', '\\')`"",
    'interval_seconds=60',
    'interval_read_config_seconds=3600',
    "hostname=`"$env:COMPUTERNAME`"",
    "instance_type=`"$InstanceType`""
)
$memoryLimit = if ($env:RELEEM_MYSQL_MEMORY_LIMIT) { $env:RELEEM_MYSQL_MEMORY_LIMIT } elseif ($env:RELEEM_DB_MEMORY_LIMIT) { $env:RELEEM_DB_MEMORY_LIMIT } else { $null }
if ($null -ne $memoryLimit) {
    $confLines += "memory_limit=$memoryLimit"
}
if ($env:RELEEM_QUERY_OPTIMIZATION) {
    $confLines += "query_optimization=$($env:RELEEM_QUERY_OPTIMIZATION)"
}
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
[System.IO.File]::WriteAllText($ConfigFilePath, ($confLines -join "`r`n"), $utf8NoBom)
Write-Log "Created releem.conf: $ConfigFilePath"

# Restrict ACL: readable only by SYSTEM and Administrators
& icacls $ConfigFilePath /inheritance:r '/grant:r' 'SYSTEM:(R)' '/grant:r' 'Administrators:(M)' | Out-Null
Write-Log 'releem.conf permissions restricted to SYSTEM and Administrators.'

# ---------------------------------------------------------------------------
# Windows Service installation and start
# ---------------------------------------------------------------------------

# Optional first-run flag check (skipped when RELEEM_AGENT_DISABLE=1)
if ($env:RELEEM_AGENT_DISABLE -ne '1') {
    Write-Log 'Running releem-agent.exe -f (initial flag check)...'
    & $AgentBinaryPath -f
    Write-Log "releem-agent.exe -f exited with code: $LASTEXITCODE"
}

$existingSvc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
if ($existingSvc) {
    Write-Log 'Releem Agent service already exists - stopping and removing...'
    & $AgentBinaryPath stop
    & $AgentBinaryPath remove
    # Wait for SCM to fully remove the service (up to 10 seconds)
    $waited = 0
    while ((Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue) -and $waited -lt 10) {
        Start-Sleep -Seconds 1
        $waited++
    }
}

Write-Log 'Installing Releem Agent Windows Service...'
& $AgentBinaryPath install
if ($LASTEXITCODE -ne 0) {
    Write-Log "ERROR: 'releem-agent.exe install' failed with exit code $LASTEXITCODE."
    $script:MainExitCode = 1; return
}
Write-Log 'Releem Agent service installed.'

Write-Log 'Starting Releem Agent Windows Service...'
& $AgentBinaryPath start
if ($LASTEXITCODE -ne 0) {
    Write-Log "ERROR: 'releem-agent.exe start' failed with exit code $LASTEXITCODE."
    $script:MainExitCode = 1; return
}
Write-Log 'Releem Agent service start command issued.'

# Verify the service is actually running
$svc = Get-Service -Name 'releem-agent' -ErrorAction SilentlyContinue
if (-not $svc -or $svc.Status -ne 'Running') {
    $svcStatus = if ($svc) { $svc.Status } else { 'not found' }
    Write-Log "ERROR: Releem Agent service is not running. Current status: $svcStatus"
    $script:MainExitCode = 1; return
}
Write-Log "Releem Agent service is running."

# ---------------------------------------------------------------------------
# Scheduled Task for daily auto-updates
# ---------------------------------------------------------------------------

# Download and save installer script for scheduled task use
Write-Log "Downloading installer script from $InstallerUrl ..."
try {
    Invoke-WebRequest -Uri $InstallerUrl -OutFile $InstallerScriptPath -UseBasicParsing
    Write-Log "Installer script saved: $InstallerScriptPath"
} catch {
    Write-Log "WARNING: Failed to download installer script. Scheduled task will download it on first run. $_"
}

# Download mysqlconfigurer script
Write-Log "Downloading mysqlconfigurer script from $ReleemAgentScriptUrl ..."
try {
    Invoke-WebRequest -Uri $ReleemAgentScriptUrl -OutFile $ConfigurerScriptPath -UseBasicParsing
    Write-Log "Mysqlconfigurer script saved: $ConfigurerScriptPath"
} catch {
    Write-Log "WARNING: Failed to download mysqlconfigurer script. $_"
}

# ---------------------------------------------------------------------------
# Enable query monitoring for local instances (equivalent to Linux: $RELEEM_COMMAND -p)
# ---------------------------------------------------------------------------

if ($InstanceType -eq 'local') {
    if (Test-Path $ConfigurerScriptPath) {
        Write-Log 'Enabling query monitoring for local instance...'
        & powershell.exe -NonInteractive -File $ConfigurerScriptPath -Configure
        Write-Log "mysqlconfigurer.ps1 -Configure exited with code: $LASTEXITCODE"
    } else {
        Write-Log 'WARNING: mysqlconfigurer.ps1 not found; skipping -Configure.'
    }
}

# Determine whether to create the scheduled task
$createTask = $false
if ($env:RELEEM_CRON_ENABLE -eq '1') {
    $createTask = $true
    Write-Log 'Scheduled Task auto-update: enabled via RELEEM_CRON_ENABLE=1.'
} elseif ($env:RELEEM_CRON_ENABLE -eq '0') {
    Write-Log 'Scheduled Task auto-update: disabled via RELEEM_CRON_ENABLE=0. Skipping.'
} else {
    $cronChoice = Read-Host 'Enable daily auto-updates? [Y/n]'
    if ($cronChoice -eq '' -or $cronChoice -eq 'Y' -or $cronChoice -eq 'y') {
        $createTask = $true
    } else {
        Write-Log 'Scheduled Task auto-update: skipped by user.'
    }
}

if ($createTask) {
    $existingTask = Get-ScheduledTask -TaskName 'ReleemAgentUpdate' -ErrorAction SilentlyContinue
    if ($existingTask) {
        Write-Log 'Scheduled Task ReleemAgentUpdate already exists. Skipping registration.'
    } else {
        $taskAction    = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NonInteractive -File `"$InstallerScriptPath`" -u"
        $taskTrigger   = New-ScheduledTaskTrigger -Daily -At '00:00'
        $taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount
        $taskSettings  = New-ScheduledTaskSettingsSet
        Register-ScheduledTask -TaskName 'ReleemAgentUpdate' -Action $taskAction -Trigger $taskTrigger -Principal $taskPrincipal -Settings $taskSettings | Out-Null
        Write-Log 'Scheduled Task ReleemAgentUpdate created: runs daily at 00:00 as SYSTEM.'
    }
}

Write-Log 'Releem Agent successfully installed. Check your dashboard at https://app.releem.com'
$script:MainExitCode = 0

} catch {
    Write-Log "ERROR: An unexpected error occurred: $_"
    Write-Log "ERROR: Stack trace: $($_.ScriptStackTrace)"
    $script:MainExitCode = 1
} finally {
    Send-InstallLog
}
