# Offline Windows configurer checks. No database, service changes, administrator
# privileges, or Pester required. Test 5 invokes this suite on Windows Server.
param(
    [ValidateSet('All', 'ExitCodes', 'Legacy', 'Update', 'InstallServices')]
    [string]$Check = 'All',
    [string]$WindowsScriptDir = (Join-Path $PSScriptRoot '../../windows')
)

$ErrorActionPreference = 'Stop'
$root = $WindowsScriptDir
$engine = (Get-Process -Id $PID).Path
$asts = @{}

function Get-ConfigurerAst {
    param([string]$Name)
    if (-not $asts.ContainsKey($Name)) {
        $tokens = $null; $parseErrors = $null
        $path = (Resolve-Path (Join-Path $root $Name)).Path
        $asts[$Name] = [Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$parseErrors)
        if ($parseErrors.Count) { throw ($parseErrors | Out-String) }
    }
    return $asts[$Name]
}

function ConvertTo-TestScriptBlock {
    param($Node, [switch]$Body)
    $text = $Node.Extent.Text
    if ($Body) { $text = $text.Substring(1, $text.Length - 2) }
    return [scriptblock]::Create($text)
}

function Assert-Code {
    param($Expected, $Actual, $Label)
    if ($Expected -ne $Actual) { throw "${Label}: expected $Expected, got $Actual" }
}

function Invoke-TestScript {
    param([string]$Path, [string[]]$Arguments = @(), [int]$ExpectedExit = 0, [string]$Pattern)
    # Windows PowerShell 5.1 converts native stderr to error records. Capture
    # expected failures without letting the caller's Stop preference abort us.
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $engine -NoProfile -File $Path @Arguments 2>&1 | Out-String
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    Assert-Code $ExpectedExit $exitCode $Path
    if ($Pattern -and $output -notmatch $Pattern) {
        throw "${Path}: expected output matching '$Pattern', got: $output"
    }
}

function Test-ConfigurerExitCodes {
    param($temp)
    $ast = Get-ConfigurerAst releem.ps1
    foreach ($name in @('Read-ReleemConfig', 'Set-ReleemConfigValue', 'Check-MySQLConfigDirectory', 'Check-MySQLConfigInclude', 'Invoke-ApplyConfig')) {
        $function = $ast.Find({ param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
        }, $true)
        if (-not $function) { throw "Missing function: $name" }
        . (ConvertTo-TestScriptBlock $function)
    }
    function Write-Log { param($Message) }
    function Find-MyIniPath { return $script:TestIniPath }
    $mysql_cnf_dir = ''
    Assert-Code 3 (Check-MySQLConfigDirectory) 'unset directory'
    $mysql_cnf_dir = Join-Path $temp 'absent'
    Assert-Code 3 (Check-MySQLConfigDirectory) 'missing directory'
    $mysql_cnf_dir = Join-Path $temp 'file.cnf'
    Set-Content $mysql_cnf_dir '[mysqld]'
    Assert-Code 3 (Check-MySQLConfigDirectory) 'file instead of directory'
    $mysql_cnf_dir = Join-Path $temp 'config with spaces'
    New-Item -ItemType Directory $mysql_cnf_dir | Out-Null
    Assert-Code 0 (Check-MySQLConfigDirectory) 'existing directory'
    $script:TestIniPath = Join-Path $temp 'my.ini'
    foreach ($content in @('[mysqld]', "# !includedir $mysql_cnf_dir", "!includedir ${mysql_cnf_dir}-other")) {
        Set-Content $script:TestIniPath $content
        Assert-Code 11 (Check-MySQLConfigInclude) "invalid include: $content"
    }
    foreach ($path in @($mysql_cnf_dir, ($mysql_cnf_dir -replace '\\', '/'), ($mysql_cnf_dir + '/'))) {
        Set-Content $script:TestIniPath "  !includedir `"$path`""
        Assert-Code 0 (Check-MySQLConfigInclude) "valid include: $path"
    }
    # Round trip the exact escaping used by install.ps1 and -Configure.
    $ConfigFilePath = Join-Path $temp 'releem.conf'
    $originalDirectory = $mysql_cnf_dir
    foreach ($directory in @('C:\ProgramData\Releem Agent\releem.conf.d', '\\server\share\releem.conf.d')) {
        foreach ($writer in @('installer', 'configure')) {
            if ($writer -eq 'installer') {
                Set-Content $ConfigFilePath ('mysql_cnf_dir="' + $directory.Replace('\', '\\') + '"')
            } else {
                Set-ReleemConfigValue -Key 'mysql_cnf_dir' -Value ($directory -replace '\\', '\\')
            }
            $mysql_cnf_dir = (Read-ReleemConfig)['mysql_cnf_dir']
            if ($mysql_cnf_dir -cne $directory) { throw "Path round trip failed: $writer / $directory" }
            foreach ($include in @($directory, $directory.Replace('\', '/'), ($directory.ToUpperInvariant() + '\'))) {
                Set-Content $script:TestIniPath "!includedir `"$include`""
                Assert-Code 0 (Check-MySQLConfigInclude) "persisted path: $writer / $include"
            }
            Set-Content $script:TestIniPath "!includedir ${directory}-other"
            Assert-Code 11 (Check-MySQLConfigInclude) 'persisted path rejects different directory'
        }
    }
    $mysql_cnf_dir = $originalDirectory
    $StagingCnfPath = Join-Path $temp 'z_aiops_mysql.cnf'
    Set-Content $StagingCnfPath '[mysqld]'
    Set-Content $script:TestIniPath '[mysqld]'
    $script:ExitCode = 0
    Invoke-ApplyConfig -Interactive $false -RestartService $false
    Assert-Code 11 $script:ExitCode 'apply rejects missing include before modification'
    $mysql_cnf_dir = Join-Path $temp 'absent'
    $script:ExitCode = 0
    Invoke-ApplyConfig -Interactive $false -RestartService $false
    Assert-Code 3 $script:ExitCode 'apply rejects missing directory'
    # Execute the actual rollback branch without entering the Windows bootstrap.
    $rollbackBranch = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.IfStatementAst] -and
        $node.Clauses[0].Item1.Extent.Text -eq '$Rollback'
    }, $true)
    if (-not $rollbackBranch) { throw 'Missing rollback branch' }
    $script:ExitCode = 0
    & (ConvertTo-TestScriptBlock $rollbackBranch.Clauses[0].Item2 -Body)
    Assert-Code 3 $script:ExitCode 'rollback rejects missing directory'
    Remove-Item $StagingCnfPath
    $script:ExitCode = 0
    Invoke-ApplyConfig -Interactive $false -RestartService $false
    Assert-Code 1 $script:ExitCode 'missing recommendation retains code 1'
    $script:TestIniPath = $null
    Assert-Code 0 (Check-MySQLConfigInclude) 'undiscovered my.ini follows Linux behavior'
    Write-Host 'PASS: Windows configurer directory and include exit codes'
}

function Test-LegacyEntryPoint {
    param($temp)
    Copy-Item (Join-Path $root 'mysqlconfigurer.ps1') $temp
    $wrapper = Join-Path $temp 'mysqlconfigurer.ps1'
    $source = Get-Content (Join-Path $root 'releem.ps1') -Raw
    $start = $source.IndexOf('[CmdletBinding()]')
    $end = $source.IndexOf('Set-StrictMode')
    $stub = $source.Substring($start, $end - $start) + @'
if (-not $Apply -or $NonInteractive -or -not $NoRestart -or $ApplyMode -ne 'dynamic' -or $ApiKey -cne 'test key with spaces') { exit 99 }
exit 11
'@
    Set-Content (Join-Path $temp 'releem.ps1') $stub
    $driver = Join-Path $temp 'invoke.ps1'
    Set-Content $driver 'param($Wrapper); & $Wrapper -a -Automatic:$false -NoRestart -ApplyMode dynamic -k "test key with spaces"; exit $LASTEXITCODE'
    Invoke-TestScript $driver -Arguments @($wrapper) -ExpectedExit 11
    Set-Content (Join-Path $temp 'releem.ps1') ($source.Substring($start, $end - $start) + 'if (-not $Apply -or -not $NonInteractive -or -not $ForceRestart) { exit 99 }; exit 0')
    Invoke-TestScript $wrapper -Arguments @('-Apply', '-NonInteractive', '-ForceRestart')
    Set-Content (Join-Path $temp 'releem.ps1') ($source.Substring($start, $end - $start) + 'if (-not $Update) { exit 99 }; exit 0')
    Invoke-TestScript $wrapper -Arguments @('-u')
    Remove-Item (Join-Path $temp 'releem.ps1')
    Invoke-TestScript $wrapper -ExpectedExit 1 -Pattern 'releem\.ps1 is\s+missing'
    Write-Host 'PASS: legacy Windows wrapper forwards arguments and exit codes'
}

function Test-ConfigurerUpdate {
    param($temp)
    $programDir = Join-Path $temp 'program with spaces'
    $dataDir = Join-Path $temp 'data'
    New-Item -ItemType Directory $programDir, $dataDir | Out-Null
    $AgentBinaryPath = Join-Path $programDir 'agent-stub.ps1'
    $InstallerScriptPath = Join-Path $programDir 'install.ps1'
    $ConfigurerScriptPath = Join-Path $programDir 'releem.ps1'
    $LegacyConfigurerScriptPath = Join-Path $programDir 'mysqlconfigurer.ps1'
    $CurrentVersionUrl = 'https://example.test/current_version_agent'
    $DownloadBaseUrl = 'https://releem.s3.us-east-1.amazonaws.com/v2'

    $canonicalSource = Get-Content (Join-Path $root 'releem.ps1') -Raw
    $start = $canonicalSource.IndexOf('$currentPrincipal = ')
    $end = $canonicalSource.IndexOf('# Logging setup:', $start)
    if ($start -lt 0 -or $end -lt 0) { throw 'Cannot locate the Windows privilege bootstrap' }
    $canonicalSource = $canonicalSource.Remove($start, $end - $start)
    $canonicalSource = $canonicalSource.Replace('C:\ProgramData\ReleemAgent', $dataDir).Replace('C:\Program Files\ReleemAgent', $programDir)
    $legacySource = Get-Content (Join-Path $root 'mysqlconfigurer.ps1') -Raw
    $installerSource = Get-Content (Join-Path $root 'install.ps1') -Raw
    $installer = Get-ConfigurerAst install.ps1
    foreach ($name in @('Install-AgentFiles')) {
        $node = $installer.Find({ param($n)
            $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name
        }, $true)
        if (-not $node) { throw "Missing installer function: $name" }
        . (ConvertTo-TestScriptBlock $node)
    }
    $update = $installer.Find({ param($n)
        $n -is [Management.Automation.Language.IfStatementAst] -and $n.Clauses[0].Item1.Extent.Text -eq '$u'
    }, $true)
    if (-not $update) { throw 'Missing installer update branch' }
    $updateBody = ConvertTo-TestScriptBlock $update.Clauses[0].Item2 -Body
    function Write-Log { param($Message) $script:messages += $Message }
    function Stop-ReleemAgentForReplacement {
        param($Reason)
        $global:releemConfigurerTestOrder += 'stop'
        if ($global:releemConfigurerTestService.Status -ne 'Absent') { $global:releemConfigurerTestService.Status = 'Stopped' }
    }
    function Get-Service { param($Name, $ErrorAction)
        if ($global:releemConfigurerTestService.Status -eq 'Absent' -or ($script:fresh -and -not $global:releemConfigurerTestService.Installed)) { return $null }
        return [pscustomobject]@{ Status = $global:releemConfigurerTestService.Status }
    }
    function Start-Service {
        [CmdletBinding()]
        param($Name)
        Assert-Code 'releem-agent' $Name 'rollback restarts the original agent service'
        $script:rollbackRestartCalls++
        $global:releemConfigurerTestOrder += 'rollback-start'
        # Restarting before even the last restoration/cleanup is unsafe.
        foreach ($path in @($InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath, $AgentBinaryPath)) {
            if ($script:expectedRestoredFiles.ContainsKey($path)) {
                Assert-Code $script:expectedRestoredFiles[$path] ([Convert]::ToBase64String([IO.File]::ReadAllBytes($path))) 'all originals restored before restart'
            } else {
                Assert-Code $false (Test-Path -LiteralPath $path) 'absent originals cleaned before restart'
            }
        }
        if ($script:restartFailure -eq 'throw') { throw 'Simulated service restart failure' }
        if ($script:restartFailure -ne 'stays-stopped') { $global:releemConfigurerTestService.Status = 'Running' }
    }
    function Copy-Item {
        [CmdletBinding()]
        param([Alias('Path')]$LiteralPath, $Destination, [switch]$Force)
        $installedPaths = @($InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath, $AgentBinaryPath)
        if ($installedPaths -contains $LiteralPath) { $script:recoveryDir = Split-Path -Parent $Destination }
        if ($installedPaths -contains $Destination) {
            if ($script:replacementFailed) {
                if ($Destination -eq $script:failRestorePath) { throw 'Simulated restore failure' }
            } else {
                $script:replacementCopies++
                if ($script:replacementCopies -eq $script:failCopyNumber) {
                    $script:replacementFailed = $true
                    if ($script:partialCopy) { [IO.File]::WriteAllText($Destination, 'partial replacement') }
                    throw 'Simulated replacement failure'
                }
            }
        }
        Microsoft.PowerShell.Management\Copy-Item @PSBoundParameters
    }
    function Invoke-WebRequest {
        [CmdletBinding()]
        param($Uri, $OutFile, [switch]$UseBasicParsing)
        $script:downloads += $Uri
        if ($Uri -eq $CurrentVersionUrl) {
            if ($Uri -eq $script:failDownloadUri) { throw 'Simulated version fetch failure' }
            $script:pointerReads++
            return [pscustomobject]@{ Content = $script:pointer }
        }
        $global:releemConfigurerTestOrder += "download:$Uri"
        if ($Uri -eq $script:failDownloadUri) { throw "Simulated download failure: $Uri" }
        if ($Uri -eq $script:missingDownloadUri) { return }
        if ($Uri -eq $script:emptyDownloadUri) { [IO.File]::WriteAllText($OutFile, ''); return }
        if ($Uri -eq $script:invalidDownloadUri) { Set-Content $OutFile 'function {'; return }
        if ($Uri -eq $script:installerOverride) { Set-Content $OutFile $installerSource; return }
        if ($Uri -notmatch ('^' + [regex]::Escape("$DownloadBaseUrl/"))) {
            throw "Unexpected download URL: $Uri"
        }
        switch ([IO.Path]::GetFileName($Uri)) {
            'releem-agent.exe' { Set-Content $OutFile @'
$global:releemConfigurerTestOrder += ($args -join ' ')
if ($args -contains 'install') { $global:releemConfigurerTestService.Installed = $true }
if ($args -contains 'start') { $global:releemConfigurerTestService.Status = 'Running' }
foreach ($path in @($InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Agent invoked before script install: $path" }
}
$global:LASTEXITCODE = 0
'@ }
            'install.ps1' { Set-Content $OutFile $installerSource }
            'releem.ps1' { Set-Content $OutFile $canonicalSource }
            'mysqlconfigurer.ps1' { Set-Content $OutFile $legacySource }
            default { throw "Unexpected download: $Uri" }
        }
    }
    function Reset-Scenario {
        $script:downloads = @(); $global:releemConfigurerTestOrder = @(); $script:messages = @()
        $script:failDownloadUri = ''; $script:missingDownloadUri = ''; $script:emptyDownloadUri = ''; $script:invalidDownloadUri = ''
        $script:installerOverride = ''
        $script:pointer = '99.0.0'; $script:pointerReads = 0
        $script:fresh = $false; $global:releemConfigurerTestService = @{ Status = 'Running'; Installed = $false }
        $script:rollbackRestartCalls = 0
        $script:expectedRestoredFiles = @{}; $script:restartFailure = ''
        $script:MainExitCode = 0
        $script:replacementCopies = 0; $script:failCopyNumber = 0; $script:replacementFailed = $false
        $script:partialCopy = $false; $script:failRestorePath = ''; $script:recoveryDir = ''
    }

    foreach ($scenario in @('legacy-only', 'both-outdated')) {
        Reset-Scenario
        Remove-Item $ConfigurerScriptPath -Force -ErrorAction SilentlyContinue
        Set-Content $LegacyConfigurerScriptPath 'exit 97'
        if ($scenario -eq 'both-outdated') { Set-Content $ConfigurerScriptPath 'exit 98' }
        & $updateBody
        Assert-Code 0 $script:MainExitCode "update: $scenario"
        Assert-Code 0 $script:pointerReads 'installer uses mutable URLs without reading a version pointer'
        foreach ($name in @('install.ps1', 'releem.ps1', 'mysqlconfigurer.ps1', 'releem-agent.exe')) {
            Assert-Code 1 @($script:downloads | Where-Object { $_ -eq "$DownloadBaseUrl/$name" }).Count "mutable $name"
        }
        Assert-Code 'start' $global:releemConfigurerTestOrder[-1] 'all downloads precede service start'
        foreach ($entryPoint in @($ConfigurerScriptPath, $LegacyConfigurerScriptPath)) {
            Invoke-TestScript $entryPoint -Pattern 'Usage: releem.ps1'
            Invoke-TestScript $entryPoint -Arguments @('-NonInteractive') -ExpectedExit 1 -Pattern '-NonInteractive can only be used together with -Apply'
        }
        Write-Host "PASS: $scenario update installs the complete mutable artifact set"
    }

    # Execute the fresh install's real download and service statements in source order.
    $freshStart = $installerSource.IndexOf("Install-AgentFiles -StopReason 'before reinstall'")
    $freshEnd = $installerSource.IndexOf('# MySQL user setup', $freshStart)
    $serviceStart = $installerSource.IndexOf('# Optional first-run flag check')
    $serviceEnd = $installerSource.IndexOf('# Enable query monitoring', $serviceStart)
    if ($freshStart -lt 0 -or $freshEnd -lt 0 -or $serviceStart -lt 0 -or $serviceEnd -lt 0) { throw 'Missing fresh install sequence' }
    $freshBody = [scriptblock]::Create($installerSource.Substring($freshStart, $freshEnd - $freshStart) + "`n" + $installerSource.Substring($serviceStart, $serviceEnd - $serviceStart))
    $freshDownloads = $installer.FindAll({ param($n)
        $n -is [Management.Automation.Language.CommandAst] -and $n.GetCommandName() -eq 'Install-AgentFiles' -and $n.Extent.StartOffset -ge $freshStart
    }, $true)
    Assert-Code 1 $freshDownloads.Count 'one complete download before fresh install service operations'
    foreach ($mode in @('update', 'fresh')) {
        foreach ($failure in @('none', 'install.ps1', 'releem.ps1', 'mysqlconfigurer.ps1', 'releem-agent.exe', 'missing', 'empty', 'empty-binary', 'syntax')) {
            Reset-Scenario
            $script:fresh = $mode -eq 'fresh'
            foreach ($path in @($AgentBinaryPath, $InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath)) { Set-Content $path 'old installed file' }
            if ($failure -eq 'missing') { $script:missingDownloadUri = "$DownloadBaseUrl/releem-agent.exe" }
            elseif ($failure -eq 'empty-binary') { $script:emptyDownloadUri = "$DownloadBaseUrl/releem-agent.exe" }
            elseif ($failure -eq 'empty') { $script:emptyDownloadUri = "$DownloadBaseUrl/releem.ps1" }
            elseif ($failure -eq 'syntax') { $script:invalidDownloadUri = "$DownloadBaseUrl/mysqlconfigurer.ps1" }
            elseif ($failure -ne 'none') { $script:failDownloadUri = "$DownloadBaseUrl/$failure" }
            $failed = $false
            try {
                if ($mode -eq 'fresh') { & $freshBody } else { & $updateBody }
            } catch { $failed = $true }
            Assert-Code ($failure -ne 'none') $failed "$mode / $failure failure"
            if ($failed) {
                if ($global:releemConfigurerTestOrder -contains 'stop' -or $global:releemConfigurerTestOrder -contains 'install' -or $global:releemConfigurerTestOrder -contains 'start') { throw "$mode touched service after $failure failure" }
                foreach ($path in @($AgentBinaryPath, $InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath)) {
                    Assert-Code "old installed file`n" ([IO.File]::ReadAllText($path).Replace("`r`n", "`n")) 'failed download preserves installed set'
                }
            } else {
                Assert-Code 'stop' $global:releemConfigurerTestOrder[4] "$mode stops only after all four downloads"
                Assert-Code 'start' $global:releemConfigurerTestOrder[-1] "$mode starts only after complete download"
            }
            Write-Host "PASS: $mode / $failure validates complete set before install/start"
        }
    }

    # A copy may fail before writing or after truncating its destination. Restore
    # each original byte-for-byte and remove files absent before installation.
    $installedPaths = @($InstallerScriptPath, $ConfigurerScriptPath, $LegacyConfigurerScriptPath, $AgentBinaryPath)
    foreach ($mode in @('update', 'fresh')) {
        foreach ($originalSet in @('complete', 'legacy-only', 'none')) {
            foreach ($boundary in 1..4) {
                foreach ($partial in @($false, $true)) {
                    Reset-Scenario
                    $script:fresh = $mode -eq 'fresh'
                    $originals = @{}
                    foreach ($path in $installedPaths) {
                        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
                        if ($originalSet -eq 'complete' -or ($originalSet -eq 'legacy-only' -and $path -in @($LegacyConfigurerScriptPath, $AgentBinaryPath))) {
                            [IO.File]::WriteAllBytes($path, [byte[]]@(0, 255, 10, $originals.Count))
                            $originals[$path] = [Convert]::ToBase64String([IO.File]::ReadAllBytes($path))
                        }
                    }
                    $script:expectedRestoredFiles = $originals
                    $script:failCopyNumber = $boundary; $script:partialCopy = $partial
                    $failureMessage = ''
                    try {
                        if ($mode -eq 'fresh') { & $freshBody } else { & $updateBody }
                    } catch { $failureMessage = $_.Exception.Message }
                    if ($failureMessage -notmatch 'Simulated replacement failure') { throw "Expected copy failure: $failureMessage" }
                    foreach ($path in $installedPaths) {
                        if ($originals.ContainsKey($path)) {
                            Assert-Code $originals[$path] ([Convert]::ToBase64String([IO.File]::ReadAllBytes($path))) "$mode restores $path after copy $boundary"
                        } else {
                            Assert-Code $false (Test-Path -LiteralPath $path) "$mode removes originally absent $path after copy $boundary"
                        }
                    }
                    if ($global:releemConfigurerTestOrder -contains 'install' -or $global:releemConfigurerTestOrder -contains 'start') { throw 'Failed replacement must not install/start the service' }
                    $expectedRestarts = if ($mode -eq 'update') { 1 } else { 0 }
                    Assert-Code $expectedRestarts $script:rollbackRestartCalls 'only an initially running service is restarted after restoration'
                    if ($expectedRestarts) { Assert-Code 'Running' $global:releemConfigurerTestService.Status 'restored agent is running again' }
                    if ($script:recoveryDir) { Assert-Code $false (Test-Path -LiteralPath $script:recoveryDir) 'successful restore cleans temporary backups' }
                }
            }
            Write-Host "PASS: $mode / $originalSet restores original set at all four copy boundaries, including partial writes"
        }
    }

    # A failed restoration must preserve all recovery copies and continue trying
    # other files, with the retained path and affected destination in the error.
    Reset-Scenario
    foreach ($path in $installedPaths) { [IO.File]::WriteAllText($path, "original: $path") }
    $script:failCopyNumber = 4; $script:partialCopy = $true
    $script:failRestorePath = $ConfigurerScriptPath
    $failureMessage = ''
    try { & $updateBody } catch { $failureMessage = $_.Exception.Message }
    try {
        if ($failureMessage -notmatch 'Simulated restore failure' -or
            -not $failureMessage.Contains($ConfigurerScriptPath) -or
            -not $script:recoveryDir -or -not $failureMessage.Contains($script:recoveryDir)) {
            throw "Missing actionable recovery error: $failureMessage"
        }
        foreach ($path in $installedPaths) {
            $name = if ($path -eq $AgentBinaryPath) { 'releem-agent.exe' } else { Split-Path -Leaf $path }
            Assert-Code "original: $path" ([IO.File]::ReadAllText((Join-Path $script:recoveryDir $name))) 'restore failure keeps every original backup'
            if ($path -ne $ConfigurerScriptPath) { Assert-Code "original: $path" ([IO.File]::ReadAllText($path)) 'restore continues after a file fails' }
        }
        if ($global:releemConfigurerTestOrder -contains 'start') { throw 'Restore failure must not start service' }
        Assert-Code 0 $script:rollbackRestartCalls 'incomplete restoration must never restart the service'
        Write-Host 'PASS: restore failure preserves recovery backups and reports recovery location'
    } finally {
        if ($script:recoveryDir -and (Test-Path -LiteralPath $script:recoveryDir)) {
            Remove-Item -LiteralPath (Split-Path -Parent $script:recoveryDir) -Recurse -Force
        }
    }

    foreach ($initialState in @('Stopped', 'Absent')) {
        foreach ($boundary in 1..4) {
            Reset-Scenario
            $global:releemConfigurerTestService.Status = $initialState
            foreach ($path in $installedPaths) { [IO.File]::WriteAllText($path, "original: $path") }
            $script:failCopyNumber = $boundary; $script:partialCopy = $true
            $failureMessage = ''
            try { Install-AgentFiles -StopReason 'test restoration' } catch { $failureMessage = $_.Exception.Message }
            if ($failureMessage -notmatch 'Simulated replacement failure') { throw "Expected replacement failure: $failureMessage" }
            Assert-Code 0 $script:rollbackRestartCalls "$initialState service must remain unstarted"
            Assert-Code $initialState $global:releemConfigurerTestService.Status 'original service state preserved'
            foreach ($path in $installedPaths) { Assert-Code "original: $path" ([IO.File]::ReadAllText($path)) 'restored originals without service restart' }
        }
        Write-Host "PASS: originally $initialState service is never restarted after restoration"
    }

    foreach ($restartFailure in @('throw', 'stays-stopped')) {
        Reset-Scenario
        foreach ($path in $installedPaths) {
            [IO.File]::WriteAllText($path, "original: $path")
            $script:expectedRestoredFiles[$path] = [Convert]::ToBase64String([IO.File]::ReadAllBytes($path))
        }
        $script:failCopyNumber = 4; $script:partialCopy = $true; $script:restartFailure = $restartFailure
        $failureMessage = ''
        try { Install-AgentFiles -StopReason 'test restart failure' } catch { $failureMessage = $_.Exception.Message }
        try {
            Assert-Code 1 $script:rollbackRestartCalls 'restart attempted once after complete restoration'
            Assert-Code 'Stopped' $global:releemConfigurerTestService.Status 'failed restart is not reported as running'
            if ($failureMessage -notmatch 'Simulated replacement failure' -or
                $failureMessage -notmatch 'restart' -or $failureMessage -notmatch 'Start-Service' -or
                -not $failureMessage.Contains($script:recoveryDir)) { throw "Missing actionable restart error: $failureMessage" }
            foreach ($path in $installedPaths) {
                Assert-Code "original: $path" ([IO.File]::ReadAllText($path)) 'restart failure preserves restored originals'
                $name = if ($path -eq $AgentBinaryPath) { 'releem-agent.exe' } else { Split-Path -Leaf $path }
                Assert-Code "original: $path" ([IO.File]::ReadAllText((Join-Path $script:recoveryDir $name))) 'restart failure retains recovery backups'
            }
            Write-Host "PASS: rollback restart failure / $restartFailure preserves restored files and recovery backups"
        } finally {
            if ($script:recoveryDir -and (Test-Path -LiteralPath $script:recoveryDir)) {
                Remove-Item -LiteralPath (Split-Path -Parent $script:recoveryDir) -Recurse -Force
            }
        }
    }

    # Execute the canonical update dispatcher with default and overridden endpoints.
    $canonical = Get-ConfigurerAst releem.ps1
    $dispatch = $canonical.Find({ param($n)
        $n -is [Management.Automation.Language.IfStatementAst] -and $n.Clauses[0].Item1.Extent.Text -eq '$Update'
    }, $true)
    $dispatchBody = ConvertTo-TestScriptBlock $dispatch.Clauses[0].Item2 -Body
    function powershell.exe {
        param($ExecutionPolicy, $File, [switch]$u)
        Assert-Code 0 $args.Count 'no extra delegation arguments'
        Assert-Code $true ([bool]$u) 'delegated update mode'
        Assert-Code 'delegation-test-key' $env:RELEEM_API_KEY 'delegated API key'
        $script:delegated++
        $global:LASTEXITCODE = 0
    }
    $previousKey = $env:RELEEM_API_KEY
    try {
        foreach ($case in @(
            @{ Version = '99.0.0'; Override = '' },
            @{ Version = '99.0.0.1'; Override = '' },
            @{ Version = '99.0.0.1'; Override = 'https://override.test/install.ps1' }
        )) {
            $override = $case.Override
            Reset-Scenario
            $script:delegated = 0
            $script:pointer = $case.Version
            $script:installerOverride = $override
            $InstallerScriptUrl = if ($override) { $override } else { "$DownloadBaseUrl/install.ps1" }
            $ScriptVersion = '1.26.2'; $apikey = 'delegation-test-key'; $script:ExitCode = 99
            & $dispatchBody
            Assert-Code 0 $script:ExitCode 'configurer update succeeded'
            Assert-Code 1 $script:delegated 'installer invoked once'
            Assert-Code 1 $script:pointerReads 'configurer reads version pointer once'
            $expectedInstaller = if ($override) { $override } else { "$DownloadBaseUrl/install.ps1" }
            Assert-Code $expectedInstaller $script:downloads[-1] 'installer uses mutable URL or explicit override'
            Write-Host "PASS: configurer delegates mutable update for $($case.Version) and honors endpoint overrides"
        }
        foreach ($failure in @('pointer', 'fetch', 'download', 'missing', 'empty', 'syntax')) {
            Reset-Scenario
            $script:delegated = 0; $InstallerScriptUrl = "$DownloadBaseUrl/install.ps1"; $script:ExitCode = 99
            $uri = "$DownloadBaseUrl/install.ps1"
            switch ($failure) {
                'pointer' { $script:pointer = '../invalid' }
                'fetch' { $script:failDownloadUri = $CurrentVersionUrl }
                'download' { $script:failDownloadUri = $uri }
                'missing' { $script:missingDownloadUri = $uri }
                'empty' { $script:emptyDownloadUri = $uri }
                'syntax' { $script:invalidDownloadUri = $uri }
            }
            & $dispatchBody
            Assert-Code 1 $script:ExitCode "configurer rejects $failure"
            Assert-Code 0 $script:delegated 'invalid update never delegates'
        }
        foreach ($bad in @('latest', '../99.0.0', '99.0.0/extra', '1.2', '1.2.3.4.5', '')) {
            Reset-Scenario
            $script:pointer = $bad; $script:delegated = 0; $script:ExitCode = 99
            & $dispatchBody
            Assert-Code 1 $script:ExitCode "configurer rejects invalid version: $bad"
            Assert-Code 0 $script:delegated 'invalid version never delegates'
            Assert-Code 1 $script:downloads.Count 'invalid version performs no installer download'
        }
        Write-Host 'PASS: configurer rejects invalid pointer and missing/empty/invalid installer'
    } finally {
        $env:RELEEM_API_KEY = $previousKey
    }
}

function Test-InstallerServiceDetection {
    param($temp)
    $ast = Get-ConfigurerAst install.ps1
    $loop = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.ForEachStatementAst] -and
            $node.Extent.Text -match 'Get-Service -Name' -and
            $node.Extent.Text -match 'MysqlServiceName'
    }, $true)
    if (-not $loop) { throw 'Installer service detection loop was not found' }
    $detectService = ConvertTo-TestScriptBlock $loop
    $assignment = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.AssignmentStatementAst] -and
            $node.Left.Extent.Text -eq '$restartCmd'
    }, $true)
    if (-not $assignment) { throw 'Installer restart command assignment was not found' }
    $buildRestartCommand = ConvertTo-TestScriptBlock $assignment
    function Write-Log { param($Message) }
    function Get-Service {
        param($Name, $ErrorAction)
        if ($Name -eq $availableService) { return [pscustomobject]@{ Name = $Name } }
    }
    foreach ($availableService in @('MySQL90', 'MySQL97', 'MySQL267', 'MySQL84', 'MySQL80', 'MySQL57', 'MySQL56', 'MySQL', 'mariadb', 'mysqld', 'unknown')) {
        $MysqlServiceName = $null
        . $detectService
        . $buildRestartCommand
        if ($availableService -eq 'unknown') {
            Assert-Code '' $restartCmd 'undetected service leaves restart command empty'
        } else {
            Assert-Code $availableService $MysqlServiceName 'detected MySQL service'
            Assert-Code "net stop $availableService && net start $availableService" $restartCmd 'generated restart command'
        }
        Write-Host "PASS: installer service detection: $availableService"
    }
}

# Parse each source only once and isolate each scenario's mocks and files.
foreach ($name in @('install.ps1', 'releem.ps1', 'mysqlconfigurer.ps1')) { $null = Get-ConfigurerAst $name }
foreach ($case in @(
    @{ Name = 'ExitCodes'; Function = 'Test-ConfigurerExitCodes' },
    @{ Name = 'Legacy'; Function = 'Test-LegacyEntryPoint' },
    @{ Name = 'Update'; Function = 'Test-ConfigurerUpdate' },
    @{ Name = 'InstallServices'; Function = 'Test-InstallerServiceDetection' }
)) {
    if ($Check -ne 'All' -and $Check -ne $case.Name) { continue }
    $temp = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())
    New-Item -ItemType Directory $temp | Out-Null
    try {
        & $case.Function $temp
    } finally {
        Remove-Variable -Name releemConfigurerTestOrder, releemConfigurerTestService -Scope Global -ErrorAction SilentlyContinue
        Remove-Item $temp -Recurse -Force
    }
}
