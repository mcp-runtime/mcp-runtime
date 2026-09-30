$ErrorActionPreference = 'Stop'

$repo = 'mcp-runtime/mcp-runtime'
$version = if ($env:MCP_RUNTIME_VERSION) { $env:MCP_RUNTIME_VERSION } else { 'latest' }
$installDir = if ($env:MCP_RUNTIME_INSTALL_DIR) { $env:MCP_RUNTIME_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }

if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw "Unsupported Windows architecture: $env:PROCESSOR_ARCHITECTURE (supported: AMD64)"
}

$asset = 'mcp-runtime-windows-amd64.exe'
if ($version -eq 'latest') {
    $url = "https://github.com/$repo/releases/latest/download/$asset"
} else {
    $url = "https://github.com/$repo/releases/download/$version/$asset"
}

$tempFile = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString() + '.exe')
try {
    Invoke-WebRequest -Uri $url -OutFile $tempFile
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Move-Item -Path $tempFile -Destination (Join-Path $installDir 'mcp-runtime.exe') -Force

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $pathEntries = @($userPath -split ';' | Where-Object { $_ })
    if ($pathEntries -notcontains $installDir) {
        $newPath = (@($pathEntries) + $installDir) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    }
    $env:Path = "$installDir;$env:Path"

    Write-Output "Installed mcp-runtime to $(Join-Path $installDir 'mcp-runtime.exe')"
    Write-Output 'Open a new terminal to use mcp-runtime from any directory.'
} finally {
    if (Test-Path $tempFile) {
        Remove-Item $tempFile -Force
    }
}
