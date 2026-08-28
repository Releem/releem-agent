# Test 11: Fresh install uses RELEEM_MYSQL_ROOT_LOGIN for automatic user creation.
# Required env vars:
#   RELEEM_API_KEY, MYSQL_ROOT_PASSWORD, OS_VERSION

param()

. "$PSScriptRoot\helpers.ps1"

$env:RELEEM_API_KEY      = if ($env:RELEEM_API_KEY) { $env:RELEEM_API_KEY } else { throw "RELEEM_API_KEY must be set" }
$env:MYSQL_ROOT_PASSWORD = if ($env:MYSQL_ROOT_PASSWORD) { $env:MYSQL_ROOT_PASSWORD } else { throw "MYSQL_ROOT_PASSWORD must be set" }
$OsVersion               = if ($env:OS_VERSION) { $env:OS_VERSION } else { throw "OS_VERSION must be set" }

$Hostname = "releem-agent-test-$OsVersion-custom-root"
$DbService = Get-MySQLServiceName
$AdminUser = "releem_admin"
$AdminPassword = "ReleemAdminPw123!"

Write-Info "=== Test 11: Fresh install with custom MySQL root login ==="
Write-Info "Hostname: $Hostname"

Remove-ReleemAgent
Remove-ReleemMySQLUser

Assert-ServiceRunning "Pre: DB service running" $DbService

& mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -e `
    "DROP USER IF EXISTS '$AdminUser'@'%'; DROP USER IF EXISTS '$AdminUser'@'localhost'; DROP USER IF EXISTS '$AdminUser'@'127.0.0.1'; CREATE USER '$AdminUser'@'%' IDENTIFIED BY '$AdminPassword'; CREATE USER '$AdminUser'@'localhost' IDENTIFIED BY '$AdminPassword'; CREATE USER '$AdminUser'@'127.0.0.1' IDENTIFIED BY '$AdminPassword'; GRANT ALL PRIVILEGES ON *.* TO '$AdminUser'@'%' WITH GRANT OPTION; GRANT ALL PRIVILEGES ON *.* TO '$AdminUser'@'localhost' WITH GRANT OPTION; GRANT ALL PRIVILEGES ON *.* TO '$AdminUser'@'127.0.0.1' WITH GRANT OPTION; FLUSH PRIVILEGES;" `
    2>$null
Assert-Zero "Pre: custom administrative user created" $LASTEXITCODE

Write-Info "Running install.ps1 with RELEEM_MYSQL_ROOT_LOGIN=$AdminUser..."
$env:RELEEM_HOSTNAME = $Hostname
$env:RELEEM_MYSQL_ROOT_LOGIN = $AdminUser
$env:RELEEM_MYSQL_ROOT_PASSWORD = $AdminPassword
$env:RELEEM_CRON_ENABLE = "1"
$env:RELEEM_DB_MEMORY_LIMIT = "0"

& powershell.exe -ExecutionPolicy Bypass -File $InstallScript
$installExit = $LASTEXITCODE

Assert-Zero "install.ps1 exited successfully with custom root login" $installExit
Assert-FileExists "releem.conf created" "C:\ProgramData\ReleemAgent\releem.conf"
Assert-ServiceRunning "releem-agent service running" "releem-agent"
Assert-MySQLUserExists "releem MySQL user created" "releem"

& mysql -u root -p"$env:MYSQL_ROOT_PASSWORD" -e `
    "DROP USER IF EXISTS '$AdminUser'@'%'; DROP USER IF EXISTS '$AdminUser'@'localhost'; DROP USER IF EXISTS '$AdminUser'@'127.0.0.1'; FLUSH PRIVILEGES;" `
    2>$null

Show-Summary "Test 11: Install with custom MySQL root login"
