param(
    [Parameter(Mandatory)][ValidateSet('0.31.0', '0.31.1')][string]$Version,
    [Parameter(Mandatory)][string]$Work
)
$ErrorActionPreference = 'Stop'
if ($env:PRINTMASTER_LIVE_LEGACY_MSI -ne 'disposable-github-runner' -or
    $env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'Native MSI migration requires an explicitly approved disposable GitHub-hosted runner.'
}
$digests = @{
    '0.31.0' = '467515862f9760761e7467e435a73862b1a549f2df18c6e998533b69ad25a6ab'
    '0.31.1' = '447fd61df0f985c0b2e8fde01f545469c30bed77f781c9fcb5bec6d42ff4cc42'
}
$baseline = Join-Path $Work 'baseline.msi'
Invoke-WebRequest -Uri "https://github.com/Printmaster-Org/printmaster/releases/download/agent-v$Version/printmaster-agent-v$Version-windows-amd64.msi" -OutFile $baseline
if ((Get-FileHash $baseline -Algorithm SHA256).Hash.ToLowerInvariant() -ne $digests[$Version]) {
    throw 'Released MSI does not match independently recorded source pin.'
}
"$($digests[$Version]) baseline.msi" | Set-Content "$Work\evidence\baseline-sha256.txt"
$zip = Join-Path $Work 'wix.zip'
Invoke-WebRequest -Uri 'https://github.com/wixtoolset/wix3/releases/download/wix3112rtm/wix311-binaries.zip' -OutFile $zip
if ((Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne '2c1888d5d1dba377fc7fa14444cf556963747ff9a0a289a3599cf09da03b9e2e') {
    throw 'WiX tool archive differs from recorded checksum.'
}
$wix = Join-Path $Work 'wix'
Expand-Archive $zip -DestinationPath $wix
$source = Join-Path $PSScriptRoot '..\build\windows\msi\printmaster-agent.wxs'
[xml]$failure = Get-Content $source -Raw
$ns = 'http://schemas.microsoft.com/wix/2006/wi'
$product = $failure.DocumentElement.SelectSingleNode("*[local-name()='Product']")
$action = $failure.CreateElement('CustomAction', $ns)
$action.SetAttribute('Id', 'RejectMigrationFixture')
$action.SetAttribute('Directory', 'SystemFolder')
$action.SetAttribute('ExeCommand', '"[SystemFolder]cmd.exe" /d /c exit 1')
$action.SetAttribute('Execute', 'deferred')
$action.SetAttribute('Impersonate', 'no')
$action.SetAttribute('Return', 'check')
[void]$product.AppendChild($action)
$sequence = $failure.CreateElement('InstallExecuteSequence', $ns)
$custom = $failure.CreateElement('Custom', $ns)
$custom.SetAttribute('Action', 'RejectMigrationFixture')
$custom.SetAttribute('After', 'InstallServices')
$custom.InnerText = 'WIX_UPGRADE_DETECTED'
[void]$sequence.AppendChild($custom)
[void]$product.AppendChild($sequence)
$failureSource = Join-Path $Work 'failure.wxs'
$failure.Save($failureSource)
foreach ($name in @('candidate', 'failure')) {
    $wxs = if ($name -eq 'failure') { $failureSource } else { $source }
    & "$wix\candle.exe" -nologo -ext WixUtilExtension '-dProductVersion=0.32.0.0' "-dAgentBinaryPath=$Work\candidate.exe" -out "$Work\$name.wixobj" $wxs
    if ($LASTEXITCODE -ne 0) { throw "WiX compile failed: $LASTEXITCODE" }
    & "$wix\light.exe" -nologo -ext WixUtilExtension -out "$Work\$name.msi" "$Work\$name.wixobj"
    if ($LASTEXITCODE -ne 0) { throw "WiX link failed: $LASTEXITCODE" }
}
Get-FileHash "$Work\candidate.exe", "$Work\candidate.msi", "$Work\failure.msi" -Algorithm SHA256 |
    Select-Object Path, Hash | ConvertTo-Json | Set-Content "$Work\evidence\candidate-sha256.json"
$env:PRINTMASTER_LIVE_LEGACY_MSI_DIR = $Work
$env:PRINTMASTER_LIVE_LEGACY_MSI_VERSION = $Version
& "$Work\migration.test.exe" -test.v -test.timeout=12m '-test.run=^TestLiveLegacyWindowsMSIMigration$' 2>&1 |
    Tee-Object "$Work\evidence\migration.txt"
if ($LASTEXITCODE -ne 0) { throw "Native MSI migration qualification failed: $LASTEXITCODE" }
