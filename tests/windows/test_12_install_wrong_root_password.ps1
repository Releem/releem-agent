# Test 12: Fresh install fails when RELEEM_MYSQL_ROOT_PASSWORD is explicitly wrong.
# Required env vars:
#   RELEEM_API_KEY, MYSQL_ROOT_PASSWORD, OS_VERSION

param()

. "$PSScriptRoot\helpers.ps1"

$env:RELEEM_API_KEY      = if ($env:RELEEM_API_KEY) { $env:RELEEM_API_KEY } else { throw "RELEEM_API_KEY must be set" }
$env:MYSQL_ROOT_PASSWORD = if ($env:MYSQL_ROOT_PASSWORD) { $env:MYSQL_ROOT_PASSWORD } else { throw "MYSQL_ROOT_PASSWORD must be set" }
$OsVersion               = if ($env:OS_VERSION) { $env:OS_VERSION } else { throw "OS_VERSION must be set" }

$Hostname = "releem-agent-test-$OsVersion-wrong-root"
$DbService = Get-MySQLServiceName

Write-Info "=== Test 12: Fresh install with wrong MySQL root password ==="
Write-Info "Hostname: $Hostname"

Remove-ReleemAgent
Remove-ReleemMySQLUser

Assert-ServiceRunning "Pre: DB service running" $DbService

Write-Info "Running install.ps1 with wrong RELEEM_MYSQL_ROOT_PASSWORD..."
$env:RELEEM_HOSTNAME = $Hostname
$env:RELEEM_MYSQL_ROOT_LOGIN = "root"
$env:RELEEM_MYSQL_ROOT_PASSWORD = "wrong-password"
$env:RELEEM_CRON_ENABLE = "1"
$env:RELEEM_DB_MEMORY_LIMIT = "0"
Remove-Item Env:RELEEM_MYSQL_LOGIN -ErrorAction SilentlyContinue
Remove-Item Env:RELEEM_MYSQL_PASSWORD -ErrorAction SilentlyContinue

& powershell.exe -ExecutionPolicy Bypass -File $InstallScript
$installExit = $LASTEXITCODE

if ($installExit -ne 0) {
    Write-Pass "install.ps1 failed with wrong root password"
} else {
    Write-Fail "install.ps1 should fail with wrong root password"
}

$count = & mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -sNe "SELECT COUNT(*) FROM mysql.user WHERE User='releem';" 2>$null
if ([int]$count -eq 0) {
    Write-Pass "releem MySQL user was not created"
} else {
    Write-Fail "releem MySQL user should not be created with wrong root password"
}

Show-Summary "Test 12: Install with wrong MySQL root password"
