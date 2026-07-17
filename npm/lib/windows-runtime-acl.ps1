param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Path,
    [Parameter(Mandatory = $true, Position = 1)]
    [ValidateSet("initialize", "verify")]
    [string]$Mode
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$systemSid = [System.Security.Principal.SecurityIdentifier]::new("S-1-5-18")
$administratorsSid = [System.Security.Principal.SecurityIdentifier]::new("S-1-5-32-544")
$allowedSids = @{}
foreach ($sid in @($currentSid, $systemSid, $administratorsSid)) {
    $allowedSids[$sid.Value] = $true
}

$item = Get-Item -LiteralPath $Path -Force
if (-not $item.PSIsContainer) {
    throw "AgentNet Windows runtime root is not a directory"
}
if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "AgentNet Windows runtime root is a reparse point"
}

if ($Mode -eq "initialize") {
    $acl = [System.Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($currentSid)
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor `
        [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
    foreach ($sid in @($currentSid, $systemSid, $administratorsSid)) {
        $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
            $sid,
            [System.Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [System.Security.AccessControl.PropagationFlags]::None,
            [System.Security.AccessControl.AccessControlType]::Allow
        )
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

$actual = Get-Acl -LiteralPath $Path
if (-not $actual.AreAccessRulesProtected) {
    throw "AgentNet Windows runtime root DACL is not protected"
}
try {
    $ownerSid = ([System.Security.Principal.NTAccount]$actual.Owner).Translate(
        [System.Security.Principal.SecurityIdentifier]
    )
} catch {
    $ownerSid = [System.Security.Principal.SecurityIdentifier]::new($actual.Owner)
}
if ($ownerSid.Value -ne $currentSid.Value) {
    throw "AgentNet Windows runtime root owner is not the current user"
}

$currentFullControl = $false
foreach ($rule in $actual.Access) {
    if ($rule.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow) {
        continue
    }
    $ruleSid = $rule.IdentityReference.Translate(
        [System.Security.Principal.SecurityIdentifier]
    ).Value
    if (-not $allowedSids.ContainsKey($ruleSid)) {
        throw "AgentNet Windows runtime root DACL grants an unapproved principal"
    }
    if ($ruleSid -eq $currentSid.Value) {
        $fullControl = [System.Security.AccessControl.FileSystemRights]::FullControl
        if (($rule.FileSystemRights -band $fullControl) -eq $fullControl) {
            $currentFullControl = $true
        }
    }
}
if (-not $currentFullControl) {
    throw "AgentNet Windows runtime root does not grant current-user full control"
}
