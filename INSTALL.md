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

