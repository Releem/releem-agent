# Test 10: Fresh install prompts for MySQL root password when RELEEM_MYSQL_ROOT_PASSWORD is unset.
# Required env vars:
#   RELEEM_API_KEY, MYSQL_ROOT_PASSWORD, OS_VERSION

param()

. "$PSScriptRoot\helpers.ps1"

$env:RELEEM_API_KEY      = if ($env:RELEEM_API_KEY) { $env:RELEEM_API_KEY } else { throw "RELEEM_API_KEY must be set" }
$env:MYSQL_ROOT_PASSWORD = if ($env:MYSQL_ROOT_PASSWORD) { $env:MYSQL_ROOT_PASSWORD } else { throw "MYSQL_ROOT_PASSWORD must be set" }
$OsVersion               = if ($env:OS_VERSION) { $env:OS_VERSION } else { throw "OS_VERSION must be set" }

$Hostname = "releem-agent-test-$OsVersion-prompt"
$DbService = Get-MySQLServiceName

Write-Info "=== Test 10: Fresh install with prompted root password ==="
Write-Info "Hostname: $Hostname"

Remove-ReleemAgent
Remove-ReleemMySQLUser

Assert-ServiceRunning "Pre: DB service running" $DbService

Remove-Item Env:RELEEM_MYSQL_ROOT_PASSWORD -ErrorAction SilentlyContinue
Remove-Item Env:RELEEM_MYSQL_LOGIN -ErrorAction SilentlyContinue
Remove-Item Env:RELEEM_MYSQL_PASSWORD -ErrorAction SilentlyContinue

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = 'powershell.exe'
$psi.Arguments = "-ExecutionPolicy Bypass -File `"$InstallScript`""
$psi.UseShellExecute = $false
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $false
$psi.RedirectStandardError = $false
$psi.CreateNoWindow = $true
$psi.EnvironmentVariables['RELEEM_API_KEY'] = $env:RELEEM_API_KEY
$psi.EnvironmentVariables['RELEEM_HOSTNAME'] = $Hostname
$psi.EnvironmentVariables['RELEEM_CRON_ENABLE'] = '1'
$psi.EnvironmentVariables['RELEEM_DB_MEMORY_LIMIT'] = '0'
$psi.EnvironmentVariables['RELEEM_MYSQL_HOST'] = '127.0.0.1'
$psi.EnvironmentVariables['RELEEM_MYSQL_PORT'] = '3306'
$psi.EnvironmentVariables.Remove('RELEEM_MYSQL_ROOT_PASSWORD')
$psi.EnvironmentVariables.Remove('RELEEM_MYSQL_LOGIN')
$psi.EnvironmentVariables.Remove('RELEEM_MYSQL_PASSWORD')

Write-Info "Running install.ps1 without RELEEM_MYSQL_ROOT_PASSWORD and feeding password to stdin..."
$proc = [System.Diagnostics.Process]::Start($psi)
$proc.StandardInput.WriteLine($env:MYSQL_ROOT_PASSWORD)
$proc.StandardInput.Close()

if (-not $proc.WaitForExit(600000)) {
    $proc.Kill()
    Write-Fail "install.ps1 completed after prompted root password: timed out"
} else {
    Assert-Zero "install.ps1 exited successfully after prompted root password" $proc.ExitCode
}

Assert-FileExists "releem.conf created" "C:\ProgramData\ReleemAgent\releem.conf"
Assert-FileExists "releem-agent.exe present" "C:\Program Files\ReleemAgent\releem-agent.exe"
Assert-ServiceRunning "releem-agent service running" "releem-agent"
Assert-MySQLUserExists "releem MySQL user created" "releem"

Show-Summary "Test 10: Install with prompted root password"
