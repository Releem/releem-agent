# Common helpers for Releem agent Windows test scripts.
# Dot-source this file in each test: . "$PSScriptRoot\helpers.ps1"

$script:TestsPassed = 0
$script:TestsFailed = 0

$InstallScript    = if ($env:INSTALL_SCRIPT)    { $env:INSTALL_SCRIPT }    else { "C:\releem_tests\install.ps1" }
$ConfigurerScript = if ($env:CONFIGURER_SCRIPT) { $env:CONFIGURER_SCRIPT } else { "C:\releem_tests\mysqlconfigurer.ps1" }

# Releem API base URL
$ReleemApiBase = if ($env:RELEEM_API_BASE) { $env:RELEEM_API_BASE } else { "https://app.releem.com/api" }

function Write-Log {
    param([string]$Level, [string]$Message)
    $ts = Get-Date -Format "HH:mm:ss"
    Write-Host "[$ts][$Level] $Message"
}
function Write-Info  { param($m) Write-Log "INFO " $m }
function Write-Pass  { param($m) Write-Log "PASS " $m; $script:TestsPassed++ }
function Write-Fail  { param($m) Write-Log "FAIL " $m; $script:TestsFailed++ }
function Write-Error { param($m) Write-Log "ERROR" $m }

function Assert-Equal {
    param([string]$Desc, [string]$Expected, [string]$Actual)
    if ($Expected -eq $Actual) {
        Write-Pass "$Desc"
    } else {
        Write-Fail "$Desc`: expected='$Expected', got='$Actual'"
    }
}

function Assert-Zero {
    param([string]$Desc, [int]$Code)
    if ($Code -eq 0) { Write-Pass $Desc } else { Write-Fail "$Desc`: expected exit 0, got $Code" }
}

function Assert-FileExists {
    param([string]$Desc, [string]$Path)
    if (Test-Path $Path -PathType Leaf) {
        Write-Pass "$Desc`: $Path exists"
    } else {
        Write-Fail "$Desc`: file not found: $Path"
    }
}

function Assert-FileNotExists {
    param([string]$Desc, [string]$Path)
    if (-not (Test-Path $Path -PathType Leaf)) {
        Write-Pass "$Desc`: $Path absent"
    } else {
        Write-Fail "$Desc`: expected file to be absent: $Path"
    }
}

function Assert-DirExists {
    param([string]$Desc, [string]$Path)
    if (Test-Path $Path -PathType Container) {
        Write-Pass "$Desc`: $Path exists"
    } else {
        Write-Fail "$Desc`: directory not found: $Path"
    }
}

function Assert-ServiceRunning {
    param([string]$Desc, [string]$ServiceName)
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -eq 'Running') {
        Write-Pass "$Desc`: service '$ServiceName' is running"
    } else {
        Write-Fail "$Desc`: service '$ServiceName' is not running (status: $($svc.Status))"
    }
}

function Assert-ServiceStopped {
    param([string]$Desc, [string]$ServiceName)
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc -or $svc.Status -ne 'Running') {
        Write-Pass "$Desc`: service '$ServiceName' is not running"
    } else {
        Write-Fail "$Desc`: service '$ServiceName' is still running"
    }
}

function Assert-MySQLUserExists {
    param([string]$Desc, [string]$User)
    $result = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -sNe "SELECT COUNT(*) FROM mysql.user WHERE User='$User';" 2>$null
    if ([int]$result -gt 0) {
        Write-Pass "$Desc`: MySQL user '$User' exists"
    } else {
        Write-Fail "$Desc`: MySQL user '$User' not found"
    }
}

function Assert-MySQLCanConnect {
    param([string]$Desc, [string]$User, [string]$Password)
    $output = & mysql -u $User -p"$Password" -h 127.0.0.1 -e "SELECT 1;" 2>&1
    if ($LASTEXITCODE -eq 0) {
        Write-Pass "$Desc`: MySQL user '$User' can connect"
    } else {
        Write-Fail "$Desc`: MySQL user '$User' cannot connect"
    }
}

function Get-ReleemConfigValue {
    param([string]$Path, [string]$Key)

    foreach ($line in Get-Content -Path $Path) {
        if ($line -match "^$([regex]::Escape($Key))=""?(.*?)""?$") {
            return $matches[1]
        }
    }
    throw "Key '$Key' not found in $Path"
}

function Assert-MySQLCanRunQuery {
    param([string]$Desc, [string]$User, [string]$Password, [string]$Query)
    $output = & mysql -u $User -p"$Password" -h 127.0.0.1 -e $Query 2>&1
    if ($LASTEXITCODE -eq 0) {
        Write-Pass $Desc
    } else {
        Write-Fail "$Desc`: MySQL user '$User' cannot run '$Query': $output"
    }
}

function Assert-FileContains {
    param([string]$Desc, [string]$FilePath, [string]$Pattern)
    if (Test-Path $FilePath) {
        $content = Get-Content $FilePath -Raw
        if ($content -match $Pattern) {
            Write-Pass "$Desc`: '$Pattern' found in $FilePath"
            return
        }
    }
    Write-Fail "$Desc`: '$Pattern' not found in $FilePath"
}

function Assert-ScheduledTaskExists {
    param([string]$Desc, [string]$TaskName)
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        Write-Pass "$Desc`: scheduled task '$TaskName' exists"
    } else {
        Write-Fail "$Desc`: scheduled task '$TaskName' not found"
    }
}

# Check that agent hostname is registered in the Releem API
function Assert-ApiRegistered {
    param([string]$Desc, [string]$Hostname)
    $apiKey = $env:RELEEM_API_KEY
    if (-not $apiKey) {
        Write-Fail "$Desc`: RELEEM_API_KEY not set"
        return
    }
    try {
        $resp = Invoke-RestMethod -Uri "$ReleemApiBase/servers" `
            -Headers @{ "x-releem-api-key" = $apiKey } `
            -TimeoutSec 30 -ErrorAction Stop
        $json = $resp | ConvertTo-Json -Depth 10
        if ($json -match [regex]::Escape($Hostname)) {
            Write-Pass "$Desc`: hostname '$Hostname' found in Releem API"
        } else {
            Write-Fail "$Desc`: hostname '$Hostname' not found in Releem API"
        }
    } catch {
        Write-Fail "$Desc`: API request failed: $_"
    }
}

function Assert-ApiConfigApplied {
    param([string]$Desc, [string]$Hostname)
    $apiKey = $env:RELEEM_API_KEY
    if (-not $apiKey) { Write-Fail "$Desc`: RELEEM_API_KEY not set"; return }
    try {
        $resp = Invoke-RestMethod -Uri "$ReleemApiBase/servers" `
            -Headers @{ "x-releem-api-key" = $apiKey } `
            -TimeoutSec 30 -ErrorAction Stop
        $json = $resp | ConvertTo-Json -Depth 10
        if (($json -match [regex]::Escape($Hostname)) -and ($json -match "applied")) {
            Write-Pass "$Desc`: config applied status confirmed for '$Hostname'"
        } else {
            Write-Fail "$Desc`: config applied status NOT confirmed for '$Hostname'"
        }
    } catch {
        Write-Fail "$Desc`: API request failed: $_"
    }
}

# Remove all Releem agent artifacts for a clean test run
function Remove-ReleemAgent {
    Write-Info "Cleaning up Releem agent..."
    Stop-Service -Name "releem-agent" -ErrorAction SilentlyContinue -Force
    Start-Sleep -Seconds 3

    # Remove Windows service
    $svc = Get-Service -Name "releem-agent" -ErrorAction SilentlyContinue
    if ($svc) {
        sc.exe delete "releem-agent" | Out-Null
        Start-Sleep -Seconds 2
    }

    # Remove scheduled task
    Unregister-ScheduledTask -TaskName "ReleemAgentUpdate" -Confirm:$false -ErrorAction SilentlyContinue

    # Reset ACLs and remove directories
    $dirs = @(
        "C:\Program Files\ReleemAgent",
        "C:\ProgramData\ReleemAgent"
    )
    foreach ($dir in $dirs) {
        if (Test-Path $dir) {
            icacls $dir /reset /T /Q 2>&1 | Out-Null
            Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
        }
    }
    Write-Info "Releem cleanup done"
}

# Remove releem MySQL user
function Remove-ReleemMySQLUser {
    & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -e `
        "DROP USER IF EXISTS 'releem'@'127.0.0.1'; DROP USER IF EXISTS 'releem'@'localhost'; DROP USER IF EXISTS 'releem'@'%'; FLUSH PRIVILEGES;" `
        2>$null
    Write-Info "Dropped releem MySQL user (if existed)"
}

# Detect the MySQL Windows service name
function Get-MySQLServiceName {
    $candidates = @("MySQL84", "MySQL80", "MySQL57", "MySQL56", "MySQL", "mariadb", "mysqld")
    foreach ($name in $candidates) {
        if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
            return $name
        }
    }
    return "MySQL"
}

# Print summary and exit
function Show-Summary {
    param([string]$TestName)
    Write-Host ""
    Write-Host "========================================="
    Write-Host "Test: $TestName"
    Write-Host "  PASSED: $script:TestsPassed"
    Write-Host "  FAILED: $script:TestsFailed"
    Write-Host "========================================="
    if ($script:TestsFailed -gt 0) { exit 1 }
    exit 0
}
