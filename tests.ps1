$ErrorActionPreference = 'Stop'
$forwarded = @(foreach ($argument in $args) {
    if ($argument -is [array]) { $argument -join ',' } else { $argument }
})
& node (Join-Path $PSScriptRoot 'scripts\run-tests.js') @forwarded
exit $LASTEXITCODE
