# IntelParser Use Cases

## Use case 1

Parsing downloaded IntelX file and save filtered data into SQLite Database

I used this path when I need to send to the client only his own leaked credentials.

### Parsing downloaded data

*Note:* Remember that by default the IntelParser save all data into `~/.intelparser.db` if you want to save the data into another path SQLite database use the parameters `--write-db` and `--write-db-uri` 

```
$ intelparser parse intelx -p ~/Downloads/Search\ 2025-03-04\ 21_29_58.zip

Indexing file=02683b43-5ef6-43e9-9ccd-96f265d7537d.txt
INFO Indexing file=04ce4dbc-7e50-4f16-a5c2-d1352dc11183.txt
INFO Indexing file=0fd9e5c9-af1a-4465-81d3-5c73ca40c1a2.txt
INFO Indexing file=073e04a6-79e9-4090-b673-3087a232627e.txt
INFO Indexing file=01b72b88-3b1f-4629-aebf-c88b2e6d120b.txt
INFO Indexing file=059d578f-9f3b-4c9b-84ef-b6408c68b655.txt
INFO Indexing file=0e4d6a87-d1a3-4cf5-a7f2-cee090a415bb.txt
...
...
...
INFO Indexing file=fd1ad29c-a525-41db-b617-68cc5c6ef951.txt
INFO Indexing file=fd5c4f3c-8b74-441e-8043-1926cba70ce6.txt
INFO Indexing file=ff34db82-cb7b-428b-bfa8-bbcd872cdbfb.txt


WARN Execution statistics
     -> Elapsed time.....: 00:00:16
     -> Files parsed.....: 104
     -> Skipped..........: 0
     -> Execution error..: 0
     -> Credentials......: 3
     -> URLs.............: 229.661
     -> E-mails..........: 23.280
```


### Extracting/filtering only needed related data (using word filter)

To this example I used 3 terms to filter the data `sec4us`, `webapi` and `hookchain`

```
$ intelparser report convert --to-file sec4us.sqlite3 --filter sec4us,webapi,hookchain

WARN Filter list: sec4us, webapi, hookchain
INFO starting conversion...
INFO Convertion status
     -> Elapsed time.....: 00:00:01
     -> Files converted..: 25
     -> Credentials......: 0
     -> URLs.............: 64
     -> E-mails..........: 0
```


## Use case 2

Parsing downloaded IntelX file and save filtered data into Elastic Search

### Parsing downloaded data

```
$ intelparser parse intelx -p ~/Downloads/Search\ 2025-03-04\ 21_29_58.zip

Indexing file=02683b43-5ef6-43e9-9ccd-96f265d7537d.txt
INFO Indexing file=04ce4dbc-7e50-4f16-a5c2-d1352dc11183.txt
INFO Indexing file=0fd9e5c9-af1a-4465-81d3-5c73ca40c1a2.txt
INFO Indexing file=073e04a6-79e9-4090-b673-3087a232627e.txt
INFO Indexing file=01b72b88-3b1f-4629-aebf-c88b2e6d120b.txt
INFO Indexing file=059d578f-9f3b-4c9b-84ef-b6408c68b655.txt
INFO Indexing file=0e4d6a87-d1a3-4cf5-a7f2-cee090a415bb.txt
...
...
...
INFO Indexing file=fd1ad29c-a525-41db-b617-68cc5c6ef951.txt
INFO Indexing file=fd5c4f3c-8b74-441e-8043-1926cba70ce6.txt
INFO Indexing file=ff34db82-cb7b-428b-bfa8-bbcd872cdbfb.txt


WARN Execution statistics
     -> Elapsed time.....: 00:00:16
     -> Files parsed.....: 104
     -> Skipped..........: 0
     -> Execution error..: 0
     -> Credentials......: 3
     -> URLs.............: 229.661
     -> E-mails..........: 23.280
```


### Extracting/filtering only needed related data (using word filter)

To this example I used only one term to filter the data `sec4us`

```
$ intelparser report elastic --elasticsearch-uri "http://10.10.10.10:9200/sec4us" --filter sec4us

WARN Filter list: sec4us
INFO starting conversion...
INFO Convertion status
     -> Elapsed time.....: 00:00:01
     -> Files converted..: 25
     -> Credentials......: 0
     -> URLs.............: 64
     -> E-mails..........: 0
```
