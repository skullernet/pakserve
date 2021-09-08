# pakserve

HTTP server written in Go to serve downloads to Quake 2 clients directly from
game server data. By using the same logic as Quake 2 server to build the search
path, HTTP clients get the same content as regular clients that download via
UDP.

Downloads can be served directly from PAK and ZIP (.pkz) archives, possibly in
compressed format, or from regular files on disk. Access control in form of
white lists is supported to prevent sensitive data (e.g., config files) from
being downloaded.

## Configuration

Server accepts configuration in JSON format. Path to configuration file must be
specified as the first (and only) command line argument. Example configuration
file contents is reproduced below. Only "SearchPaths" parameter is mandatory.

```json
{
    "Listen": ":8080",
    "ContentType": "application/x-quake2-data",
    "RefererCheck": "^quake2://",
    "PakWhiteList": [ "" ],
    "DirWhiteList": [
        "^(players|models|sprites|sound|maps|textures|env|pics)/",
        "^[\\w\\-]*[.]filelist$",
        "^[\\w\\-]+[.](pak|pkz)$"
    ],
    "SearchPaths": {
        "^/(baseq2/|openffa/|opentdm/)?": ["/home/user/quake2/baseq2"],
        "^/ctf/": ["/home/user/quake2/ctf", "/home/user/quake2/baseq2"]
    },
    "LogLevel": 2,
    "LogTimeStamps": true
}
```

### Listen
IP address to bind to in "[host]:port" format. Default is ":8080".

### ContentType
Reply with this content type header. Default is "application/octet-stream".

### RefererCheck
Regular expression to check HTTP referer and return 403 if it doesn't match.
Default is "" (allow any referer).

### PakWhiteList
Array of regular expressions that describe quake paths that are searched in
packfiles. Default is single entry "" (permit everything).

### DirWhiteList
Array of regular expressions that describe quake paths that are searched in
directories. Default is empty array (forbid everything).

### SearchPaths
Maps regular expressions to arrays of search paths. Each regular expression is
matched with initial part of the full path specified in request URL. Matched
part is then removed from the request path to obtain quake path. Resulting
quake path is then passed to whitelist checks and is searched in packfiles or
directories.

Each regular expression must match beginning of the string and must match the
slash character at the end (because quake path can't begin with a slash).

Note that incoming paths are always converted to lower case before processing.
Thus, names of all files on disk must be converted to lower case if file system
is case sensitive. Case of filenames inside packfiles doesn't matter (searching
in packfiles is case insensitive). Similarly, all regular expressions should
match lower case strings only.

If multiple regular expressions match the request path, the longest match wins.

Care should be taken when serving downloads with "game" variable unset on the
Quake 2 server. Some clients properly use "baseq2" as gamedir, which results in
request paths like this:


```
/baseq2/maps/q2dm1.bsp
/baseq2.filelist
```

However, some clients use an empty string in place of gamedir, which results in
paths like this:

```
/maps/q2dm1.bsp
/.filelist
```

Using catch-all regular expression such as `^/(baseq2/)?` is recommended in
this case.

### LogLevel
If ≥ 1, log search paths. If ≥ 2, log requests on stderr. By default only
errors are logged.

### LogTimeStamps
If "true", prefix log lines with time stamps. Default "false".

## Caveats

* Modifying or removing packfiles while server is running will cause bad things
  to happen. Regular files on disk can be added/removed/modified anytime.
* Server does not dynamically compress content. Data must be pre-compressed and
  stored in .pkz for this to work. However, server *will* dynamically
  decompress content if client doesn't support compression.