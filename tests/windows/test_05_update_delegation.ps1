# Test 5: mysqlconfigurer.ps1 -Update delegates update work to install.ps1 -u.

param()

. "$PSScriptRoot\helpers.ps1"

$ConfigurerUnderTest = if ($env:CONFIGURER_SCRIPT) { $env:CONFIGURER_SCRIPT } else { throw "CONFIGURER_SCRIPT must be set" }

Write-Info "=== Test 5: mysqlconfigurer.ps1 delegates update to install.ps1 -u ==="

$programDir = "C:\Program Files\ReleemAgent"
$dataDir    = "C:\ProgramData\ReleemAgent"
$markerPath = Join-Path $dataDir "update-delegation-marker.txt"
$configPath = Join-Path $dataDir "releem.conf"

Remove-ReleemAgent

New-Item -ItemType Directory -Path $programDir -Force | Out-Null
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
Remove-Item -Path $markerPath -Force -ErrorAction SilentlyContinue

@'
apikey="delegation-test-key"
'@ | Set-Content -Path $configPath -Encoding UTF8

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

$port = Get-FreeTcpPort
$stubInstaller = @"
param([switch]`$u, [switch]`$Uninstall)
Set-Content -Path '$markerPath' -Value ('u=' + `$u.ToString() + ';api=' + `$env:RELEEM_API_KEY) -Encoding UTF8
if (-not `$u) { exit 9 }
exit 0
"@

$serverJob = Start-Job -ArgumentList $port, $stubInstaller -ScriptBlock {
    param($ListenPort, $InstallerBody)

    $listener = [System.Net.HttpListener]::new()
    $listener.Prefixes.Add("http://127.0.0.1:$ListenPort/")
    $listener.Start()

    try {
        for ($i = 0; $i -lt 2; $i++) {
            $context  = $listener.GetContext()
            $response = $context.Response

            switch ($context.Request.RawUrl) {
                '/v2/current_version_agent' {
                    $body = '1.25.1'
                    $response.ContentType = 'text/plain'
                    $response.StatusCode = 200
                }
                '/v2/install.ps1' {
                    $body = $InstallerBody
                    $response.ContentType = 'text/plain'
                    $response.StatusCode = 200
                }
                default {
                    $body = 'not found'
                    $response.ContentType = 'text/plain'
                    $response.StatusCode = 404
                }
            }

            $bytes = [System.Text.Encoding]::UTF8.GetBytes($body)
            $response.ContentLength64 = $bytes.Length
            $response.OutputStream.Write($bytes, 0, $bytes.Length)
            $response.OutputStream.Close()
        }
    } finally {
        $listener.Stop()
        $listener.Close()
    }
}

$env:RELEEM_CURRENT_VERSION_URL  = "http://127.0.0.1:$port/v2/current_version_agent"
$env:RELEEM_INSTALLER_SCRIPT_URL = "http://127.0.0.1:$port/v2/install.ps1"

try {
    & powershell.exe -ExecutionPolicy Bypass -File $ConfigurerUnderTest -Update
    $updateExit = $LASTEXITCODE

    Assert-Zero "mysqlconfigurer.ps1 -Update exited successfully" $updateExit
    Assert-FileExists "Delegated installer marker created" $markerPath
    Assert-FileContains "Delegated installer received -u switch" $markerPath 'u=True'
    Assert-FileContains "Delegated installer inherited RELEEM_API_KEY" $markerPath 'api=delegation-test-key'

    $jobResult = Wait-Job -Job $serverJob -Timeout 10
    if (-not $jobResult) {
        Write-Fail "Update test HTTP server did not finish in time"
    } elseif ($serverJob.State -ne 'Completed') {
        Write-Fail "Update test HTTP server exited unexpectedly with state '$($serverJob.State)'"
    } else {
        Write-Pass "Update test HTTP server served expected requests"
    }
} finally {
    Remove-Job -Job $serverJob -Force -ErrorAction SilentlyContinue
    Remove-Item Env:RELEEM_CURRENT_VERSION_URL -ErrorAction SilentlyContinue
    Remove-Item Env:RELEEM_INSTALLER_SCRIPT_URL -ErrorAction SilentlyContinue
}

Show-Summary "Test 5: mysqlconfigurer.ps1 update delegation"
