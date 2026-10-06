param([string]$Action, [string]$InstallDir, [switch]$SelfTest)
$ErrorActionPreference = 'Stop'

# Compare directory entries without rewriting any unrelated text or expanding
# variables in the stored PATH. Registry access avoids NSIS's string-size limit.
function Update-AgentNetPath([AllowNull()][string]$Current, [string]$Directory, [bool]$Add) {
    $target = $Directory.TrimEnd('\')
    $entries = if ($Current) { $Current.Split(';') } else { @() }
    $kept = @()
    $found = $false
    foreach ($entry in $entries) {
        $resolved = [Environment]::ExpandEnvironmentVariables($entry.Trim().Trim('"')).TrimEnd('\')
        if ([string]::Equals($resolved, $target, [StringComparison]::OrdinalIgnoreCase)) {
            $found = $true
            if ($Add) { return $Current }
        } else {
            $kept += $entry
        }
    }
    if ($Add) {
        if ($Current) { return $Current + ';' + $Directory }
        return $Directory
    }
    if (-not $found) { return $Current }
    return $kept -join ';'
}

if ($SelfTest) {
    $dir = 'C:\Users\Bohdan Name\AppData\Local\AgentNet'
    $other = '%USERPROFILE%\bin;C:\Windows\System32;;C:\Other'
    if ((Update-AgentNetPath $other $dir $true) -cne ($other + ';' + $dir)) { throw 'add changed other entries' }
    $path = $other + ';"' + $dir.ToUpper() + '\"'
    if ((Update-AgentNetPath $path $dir $true) -cne $path) { throw 'duplicate add' }
    if ((Update-AgentNetPath $path $dir $false) -cne $other) { throw 'remove changed other entries' }
    if ((Update-AgentNetPath ($dir + '\tools;' + $dir) $dir $false) -cne ($dir + '\tools')) { throw 'removed another directory' }
    if ((Update-AgentNetPath $other $dir $false) -cne $other) { throw 'absent remove changed PATH' }
    if ((Update-AgentNetPath '' $dir $true) -cne $dir) { throw 'empty add' }
    if ((Update-AgentNetPath $dir $dir $false) -ne '') { throw 'sole entry remove' }
    $long = ('C:\other;' * 1000) + '%USERPROFILE%\bin'
    if ((Update-AgentNetPath ($long + ';' + $dir) $dir $false) -cne $long) { throw 'long PATH truncated' }
    Write-Output 'AgentNet user PATH checks passed'
    exit 0
}

if ($Action -notin @('add', 'remove') -or -not [IO.Path]::IsPathRooted($InstallDir)) { throw 'invalid PATH update' }
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if (-not $key) {
    if ($Action -eq 'remove') { exit 0 }
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
}
try {
    $current = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    if ($null -eq $current -and $Action -eq 'remove') { exit 0 }
    $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
    if ($null -ne $current) {
        $kind = $key.GetValueKind('Path')
        if ($kind -notin @([Microsoft.Win32.RegistryValueKind]::String, [Microsoft.Win32.RegistryValueKind]::ExpandString)) { throw 'unsupported PATH value type' }
    }
    $next = Update-AgentNetPath $current $InstallDir ($Action -eq 'add')
    if ($next -cne $current) { $key.SetValue('Path', $next, $kind) }
} finally {
    $key.Dispose()
}
