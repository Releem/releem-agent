# Test 1: Fresh Releem Agent installation with automatic releem MySQL user creation (Windows).
# Pre-conditions:
#   - MySQL/MariaDB is running with world DB loaded
#   - releem MySQL user does NOT exist
#   - No previous Releem installation
# Required env vars:
#   RELEEM_API_KEY, MYSQL_ROOT_PASSWORD, OS_VERSION

param()

. "$PSScriptRoot\helpers.ps1"

$env:RELEEM_API_KEY    = if ($env:RELEEM_API_KEY)    { $env:RELEEM_API_KEY }    else { throw "RELEEM_API_KEY must be set" }
$env:MYSQL_ROOT_PASSWORD = if ($env:MYSQL_ROOT_PASSWORD) { $env:MYSQL_ROOT_PASSWORD } else { throw "MYSQL_ROOT_PASSWORD must be set" }
$OsVersion = if ($env:OS_VERSION) { $env:OS_VERSION } else { throw "OS_VERSION must be set" }

$Hostname  = "releem-agent-test-$OsVersion"
$DbService = Get-MySQLServiceName

Write-Info "=== Test 1: Fresh installation with automatic releem user creation ==="
Write-Info "Hostname: $Hostname"

# --- Pre-test cleanup ---
Remove-ReleemAgent
Remove-ReleemMySQLUser

# --- Verify pre-conditions ---
Assert-ServiceRunning "Pre: DB service running" $DbService

$count = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -sNe "SELECT COUNT(*) FROM mysql.user WHERE User='releem';" 2>$null
if ([int]$count -gt 0) {
    Write-Fail "Pre: releem MySQL user should not exist before test"
    exit 1
}
Write-Info "Pre-conditions OK: DB running, no releem user"

# --- Run install.ps1 ---
Write-Info "Running install.ps1..."
$env:RELEEM_HOSTNAME           = $Hostname
$env:RELEEM_MYSQL_ROOT_PASSWORD = $env:MYSQL_ROOT_PASSWORD
$env:RELEEM_CRON_ENABLE        = "1"
$env:RELEEM_DB_MEMORY_LIMIT    = "0"

& powershell.exe -ExecutionPolicy Bypass -File $InstallScript
$installExit = $LASTEXITCODE

Assert-Zero "install.ps1 exited successfully" $installExit

# --- Local assertions ---
Assert-FileExists  "releem.conf created"          "C:\ProgramData\ReleemAgent\releem.conf"
Assert-DirExists   "releem conf.d dir created"    "C:\ProgramData\ReleemAgent\conf.d"
Assert-FileExists  "releem-agent.exe present"     "C:\Program Files\ReleemAgent\releem-agent.exe"
Assert-ServiceRunning "releem-agent service running" "releem-agent"
Assert-ScheduledTaskExists "ReleemAgentUpdate task created" "ReleemAgentUpdate"

if (Test-Path "C:\ProgramData\ReleemAgent\releem.conf") {
    $confText = Get-Content "C:\ProgramData\ReleemAgent\releem.conf" -Raw
    if ($confText -match [regex]::Escape($Hostname) -or $confText -match [regex]::Escape($env:COMPUTERNAME)) {
        Write-Pass "releem.conf has hostname"
    } else {
        Write-Fail "releem.conf has hostname: neither '$Hostname' nor '$($env:COMPUTERNAME)' found"
    }
} else {
    Write-Fail "releem.conf has hostname: config file missing"
}
Assert-FileContains "releem.conf has api key"   "C:\ProgramData\ReleemAgent\releem.conf" $env:RELEEM_API_KEY
Assert-FileContains "releem.conf has mysql restart service" "C:\ProgramData\ReleemAgent\releem.conf" "net stop $DbService && net start $DbService"

Assert-MySQLUserExists "releem MySQL user created" "releem"

$ReleemMysqlUser = Get-ReleemConfigValue -Path "C:\ProgramData\ReleemAgent\releem.conf" -Key "mysql_user"
$ReleemMysqlPassword = Get-ReleemConfigValue -Path "C:\ProgramData\ReleemAgent\releem.conf" -Key "mysql_password"
$ReplicaStatusQuery = "SHOW SLAVE STATUS"
$DbVersionString = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -sNe "SELECT VERSION()" 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Fail "Could not detect the database version for replication privilege checks"
} elseif ($DbVersionString -match "MariaDB") {
    $ReplicaStatusQuery = "SHOW ALL REPLICAS STATUS"
    $null = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -e $ReplicaStatusQuery 2>$null
    if ($LASTEXITCODE -ne 0) {
        $ReplicaStatusQuery = "SHOW ALL SLAVES STATUS"
    }
} else {
    $ReplicaStatusQuery = "SHOW REPLICA STATUS"
    $null = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -e $ReplicaStatusQuery 2>$null
    if ($LASTEXITCODE -ne 0) {
        $ReplicaStatusQuery = "SHOW SLAVE STATUS"
    }
}
Assert-MySQLCanRunQuery "releem user can read replication status" $ReleemMysqlUser $ReleemMysqlPassword $ReplicaStatusQuery

$GroupMembersTableExists = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -sNe `
    "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='performance_schema' AND table_name='replication_group_members';" 2>$null
$GroupMembersProbeExit = $LASTEXITCODE
if ($GroupMembersProbeExit -ne 0) {
    Write-Fail "Could not check whether performance_schema.replication_group_members exists"
} elseif ([int]$GroupMembersTableExists -gt 0) {
    Assert-MySQLCanRunQuery "releem user can read Group Replication members" $ReleemMysqlUser $ReleemMysqlPassword `
        "SELECT COUNT(*) FROM performance_schema.replication_group_members"
}

Show-Summary "Test 1: Fresh install with auto user creation (Windows)"
