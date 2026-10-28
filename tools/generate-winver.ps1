param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('agent', 'server')]
    [string]$Component,
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [Parameter(Mandatory = $true)]
    [string]$GitCommit,
    [Parameter(Mandatory = $true)]
    [string]$BuildTime,
    [Parameter()]
    [string]$OutputPath
)

$ErrorActionPreference = 'Stop'

$versionMatch = [regex]::Match($Version, '^(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)(?:\.(?<build>\d+))?')
if (-not $versionMatch.Success) {
    throw "Version '$Version' does not begin with a numeric x.y.z version"
}

$numericVersion = @(
    [int]$versionMatch.Groups['major'].Value,
    [int]$versionMatch.Groups['minor'].Value,
    [int]$versionMatch.Groups['patch'].Value,
    $(if ($versionMatch.Groups['build'].Success) { [int]$versionMatch.Groups['build'].Value } else { 0 })
)
if ($numericVersion | Where-Object { $_ -lt 0 -or $_ -gt 65535 }) {
    throw 'Windows version components must be between 0 and 65535'
}

$versionParts = @{
    Major = $numericVersion[0]
    Minor = $numericVersion[1]
    Patch = $numericVersion[2]
    Build = $numericVersion[3]
}
$description = if ($Component -eq 'server') { 'Central management server for PrintMaster agents' } else { 'Remote fleet management agent for PrintMaster' }
$originalFilename = "printmaster-$Component.exe"
$productName = if ($Component -eq 'server') { 'PrintMaster Server' } else { 'PrintMaster Agent' }
$resourceInfo = [ordered]@{
    VarFileInfo = @{
        Translation = @{ CharsetID = '04B0'; LangID = '0409' }
    }
    FixedFileInfo = @{
        FileVersion = $versionParts
        ProductVersion = $versionParts
        FileFlagsMask = '3f'
        FileType = '01'
        FileSubtype = '00'
        'FileFlags ' = '00'
        FileOS = '040004'
    }
    StringFileInfo = @{
        ProductVersion = $Version
        FileVersion = $Version
        CompanyName = 'PrintMaster'
        OriginalFilename = $originalFilename
        FileDescription = $description
        InternalName = "printmaster-$Component"
        ProductName = $productName
        LegalCopyright = 'Copyright (c) PrintMaster'
        Comments = "Build: $BuildTime, Commit: $GitCommit"
    }
}

$tempDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "printmaster-winres-$([guid]::NewGuid().ToString('N'))"
$generatedFile = Join-Path $tempDirectory 'resource.syso'
$jsonFile = Join-Path $tempDirectory 'versioninfo.json'
$outputFile = if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    Join-Path (Split-Path $PSScriptRoot -Parent) "$Component\resource.syso"
} else {
    [System.IO.Path]::GetFullPath($OutputPath)
}

try {
    New-Item -ItemType Directory -Path $tempDirectory | Out-Null
    $json = $resourceInfo | ConvertTo-Json -Depth 8
    [System.IO.File]::WriteAllText($jsonFile, $json, [System.Text.UTF8Encoding]::new($false))

    Push-Location $tempDirectory
    try {
        & go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 -o $generatedFile
        if ($LASTEXITCODE -ne 0) {
            throw "goversioninfo failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }

    if (-not (Test-Path $generatedFile)) {
        throw 'goversioninfo did not create a resource object'
    }
    $bytes = [System.IO.File]::ReadAllBytes($generatedFile)
    if ($bytes.Length -lt 20 -or [BitConverter]::ToUInt16($bytes, 0) -ne 0x8664) {
        throw 'generated resource object is not a valid Windows amd64 COFF object'
    }

    Move-Item -LiteralPath $generatedFile -Destination $outputFile -Force
    Write-Output "Generated amd64 Windows version resource: $outputFile"
} finally {
    if (Test-Path $tempDirectory) {
        Remove-Item -LiteralPath $tempDirectory -Recurse -Force
    }
}