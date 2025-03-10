# IntelParser instalation procedures 

## Linux

```
apt install curl jq

url=$(curl -s https://api.github.com/repos/helviojunior/intelparser/releases | jq -r '[ .[] | {id: .id, tag_name: .tag_name, assets: [ .assets[] | select(.name|match("linux-amd64.tar.gz$")) | {name: .name, browser_download_url: .browser_download_url} ]} | select(.assets != []) ] | sort_by(.id) | reverse | first(.[].assets[]) | .browser_download_url')

cd /tmp
rm -rf intelparser-latest.tar.gz intelparser
wget -nv -O intelparser-latest.tar.gz "$url"
tar -xzf intelparser-latest.tar.gz

rsync -av intelparser /usr/local/sbin/
chmod +x /usr/local/sbin/intelparser

intelparser version
```

## MacOS

### Installing HomeBrew

Note: Just run this command if you need to install HomeBrew to first time

```
/bin/bash -c "$(curl -fsSL raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

### Installing Intel Parser

```
brew install curl jq

arch=$(if [[ "$(uname -m)" -eq "x86_64" ]]; then echo "amd64"; else echo "arm64"; fi)

url=$(curl -s https://api.github.com/repos/helviojunior/intelparser/releases | jq -r --arg filename "darwin-${arch}.tar.gz\$" '[ .[] | {id: .id, tag_name: .tag_name, assets: [ .assets[] | select(.name|match($filename)) | {name: .name, browser_download_url: .browser_download_url} ]} | select(.assets != []) ] | sort_by(.id) | reverse | first(.[].assets[]) | .browser_download_url')

cd /tmp
rm -rf intelparser-latest.tar.gz intelparser
curl -sS -L -o intelparser-latest.tar.gz "$url"
tar -xzf intelparser-latest.tar.gz

rsync -av intelparser /usr/local/sbin/
chmod +x /usr/local/sbin/intelparser

intelparser version
```

## Windows

Just run the following powershell script

```
 # Download latest helviojunior/intelparser release from github

function Invoke-DownloadIntelParser {

    $repo = "helviojunior/intelparser"
    $file = "intelparser-latest.zip"

    Write-Host Getting release list
    $releases = "https://api.github.com/repos/$repo/releases"

    $asset = Invoke-WebRequest $releases | ConvertFrom-Json | Sort-Object -Descending -Property "Id" | ForEach-Object -Process { Get-AssetData -release $_ } | Select-Object -First 1

    if ($asset -eq $null -or $asset.browser_download_url -eq $null){
        Write-Error " Cannot find a valid URL"
        Return
    }

    Write-Host Dowloading latest release
    $zip = Join-Path -Path $ENV:Temp -ChildPath $file
    Remove-Item $zip -Force -ErrorAction SilentlyContinue 
    Invoke-WebRequest $asset.browser_download_url -Out $zip

    Write-Host Extracting release files
    Expand-Archive $zip -Force -DestinationPath $ENV:Temp

    $dwnPath = (New-Object -ComObject Shell.Application).NameSpace('shell:Downloads').Self.Path
    $name = Join-Path -Path $dwnPath -ChildPath "intelparser.exe"

    # Cleaning up target dir
    Remove-Item $name -Recurse -Force -ErrorAction SilentlyContinue 

    # Moving from temp dir to target dir
    Move-Item $(Join-Path -Path $ENV:Temp -ChildPath "intelparser.exe") -Destination $name -Force

    # Removing temp files
    Remove-Item $zip -Force
    
    Write-Host "Intel Parser saved at $name" -ForegroundColor DarkYellow

    Write-Host "Getting IntelParser version banner"
    . $name version 
    #Start-Process -FilePath $name -ArgumentList "version" -NoNewWindow -Wait -RedirectStandardOutput 1 -RedirectStandardError 2
}


Function Get-AssetData {
    [CmdletBinding(SupportsShouldProcess = $False)]
    [OutputType([object])]
    Param (
        [Parameter(Mandatory = $True, Position = 0, ParameterSetName='Release')]
        [object]$Release
    )

    if($Release -is [system.array]){
        $Release = $Release[0]
    }

    if (Get-Member -inputobject $Release -name "assets" -Membertype Properties) {
        # Determine OS and Architecture
        $osPlatform = [System.Runtime.InteropServices.RuntimeInformation]::OSDescription
        $architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture

        # Adjust the platform and architecture for the API call
        $platform = switch -Wildcard ($osPlatform) {
            "*Windows*" { "windows" }
            "*Linux*"   { "linux" }
            "*Darwin*"  { "darwin" } # MacOS is identified as Darwin
            Default     { "unknown" }
        }
        $arch = switch ($architecture) {
            "X64"  { "amd64" }
            "X86"  { "386" }
            "Arm"  { "arm" }
            "Arm64" { "arm64" }
            Default { "unknown" }
        }

        if ($platform -eq "unknown" -or $arch -eq "unknown") {
            Return $null
        }

        $extension = switch -Wildcard ($osPlatform) {
            "*Windows*" { ".zip" }
            "*Linux*"   { "tar.gz" }
            "*Darwin*"  { "tar.gz" } # MacOS is identified as Darwin
            Default     { "unknown" }
        }

        foreach ($asset in $Release.assets)
        {
            If ($asset.name.Contains("intelparser-") -and $asset.name.Contains("$platform-$arch$extension")) { Return $asset }
        }

    }
    Return $null
}

Invoke-DownloadIntelParser 
```
