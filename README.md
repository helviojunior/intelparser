# Intel Parser

IntelParser is a modular Leaks and Intelligence files parser with threading! 

Available modules/parsers:

* [x] IntelX (ZIP Downloads)

## Some amazing features

* [x] Pause and resume testing at any time.  
* [x] Parse several file patterns.  
* [x] Utilize multi-threading for faster performance.
* [x] And much more!  

## Writers

* [x] SQLite3  
* [x] CSV.  
* [x] JSON.
* [x] Elasticsearch.
* [x] And much more!  

# Build

Clone the repository and build the project with Golang:

```
git clone https://github.com/helviojunior/intelparser.git
cd intelparser
go get ./...
go build
```

If you want to update go.sum file just run the command `go mod tidy`.

# Installing system wide

After build run the commands bellow

```
go install .
ln -s /root/go/bin/intelparser /usr/bin/intelparser
```

# Utilization

```
$ intelparser parse -h


  _____       _       _ _____
 |_   _|     | |     | |  __ \
   | |  _ __ | |_ ___| | |__) |_ _ _ __ ___  ___ _ __
   | | | '_ \| __/ _ \ |  ___/ _' | '__/ __|/ _ \ '__|
  _| |_| | | | ||  __/ | |  | (_| | |  \__ \  __/ |
 |_____|_| |_|\__\___|_|_|   \__,_|_|  |___/\___|_|


Usage:
  intelparser parse [command]

Examples:

   - intelparser parse intelx -p "~/Desktop/Search 2025-02-05 10_48_28.zip"
   - intelparser parse intelx -p "~/Desktop/"
   - intelparser parse intelx -p ~/Desktop/ --write-elastic --write-elasticsearch-uri "http://127.0.0.1:9200/intelparser"


Available Commands:
  intelx      Parse IntelX downloaded files

Flags:
      --disable-control-db               Disable utilization of database ~/.intelparser.db.
  -h, --help                             help for parse
  -t, --threads int                      Number of concurrent threads (goroutines) to use (default 10)
      --write-csv                        Write results as CSV (has limited columns)
      --write-csv-file string            The file to write CSV rows to (default "intelparser.csv")
      --write-db                         Write results to a SQLite database
      --write-db-enable-debug            Enable database query debug logging (warning: verbose!)
      --write-db-uri string              The database URI to use. Supports SQLite, Postgres, and MySQL (e.g., postgres://user:pass@host:port/db) (default "sqlite:///intelparser.sqlite3")
      --write-elastic                    Write results to a SQLite database
      --write-elasticsearch-uri string   The elastic search URI to use. (e.g., http://user:pass@host:9200/index) (default "http://localhost:9200/intelparser")
      --write-jsonl                      Write results as JSON lines
      --write-jsonl-file string          The file to write JSON lines to (default "intelparser.jsonl")
      --write-none                       Use an empty writer to silence warnings
      --write-stdout                     Write successful results to stdout (usefull in a shell pipeline)

Global Flags:
  -D, --debug-log   Enable debug logging
  -q, --quiet       Silence (almost all) logging

Use "intelparser parse [command] --help" for more information about a command.

```

## Linux environment

Follows the suggest commands to install linux environment

### Installing Go v1.23.5

```
wget https://go.dev/dl/go1.23.5.linux-amd64.tar.gz
rm -rf /usr/local/go && tar -C /usr/local -xzf go1.23.5.linux-amd64.tar.gz
rm -rf /usr/bin/go && ln -s /usr/local/go/bin/go /usr/bin/go
```

## Execation with ELK benchmark

```bash
# Compressed files size
$ du -skh ~/Leaks_zip/
1.8G  ~/Leaks_zip/

# Extracted files size
$ du -skh ~/Leaks_extracted/
5.9G  ~/Leaks_extracted/

# Number of files
find ~/Leaks_extracted/ -type f | wc -l
    1746

$ intelparser parse intelx -p ~/Leaks_zip/ --write-elastic --write-elasticsearch-uri 'http://10.10.10.10:9200/test'

WARN Execution statistics
     -> Elapsed time.....: 00:29:56
     -> Files parsed.....: 1.721
     -> Skipped..........: 102
     -> Execution error..: 4
     -> Credentials......: 7.452.211
     -> URLs.............: 22.446.484
     -> E-mails..........: 21.817.512

```

![elk indices](https://github.com/helviojunior/intelparser/blob/main/images/elk.jpg "ELK Indices")

See the video bellow using intelparser to parse and ingest IntelX downloaded files to Elastic Search and see it using Kibana

[Click here to watch the video](https://www.youtube.com/watch?v=qwZNj_mNHMI)


## Disclaimer

This tool is intended for educational purpose or for use in environments where you have been given explicit/legal authorization to do so.