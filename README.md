# dkwws

Upload one self-contained file — usually an HTML plan an agent just wrote — to
S3-compatible object storage and get back a link you can paste to someone else.

```console
$ dkwws upload plan.html
uploading plan.html (41231 bytes) to dkwws
link expires 2026-09-23 12:04 UTC
https://dkwws.psimms.de/s/ycdpbvvbdtentlk57ixealxnhwixeg6h/plan.html
```

Stdout is the URL and nothing else, so an agent can capture it directly.
Progress and errors go to stderr.

Links carry 160 bits of randomness, expire after 30 days and can be renewed
without uploading the file again. The bucket itself stays private: nothing is
ever readable without going through the viewer.

## How it works

Two binaries and a private bucket, no database.

| Binary | Runs on | Credentials |
| --- | --- | --- |
| `dkwws` | each trusted machine | its own upload-scoped key pair |
| `dkwws-viewer` | one public HTTP service | one read-only key pair |

The bucket holds two kinds of object:

```
objects/<object-id>        the uploaded file, stored once and kept indefinitely
links/<link-token>.json    a link record pointing at one object
```

A link record names the object, its filename, content type, size, SHA-256 and
an expiry. Renewing a link writes a second small record next to the first,
pointing at the same object — the file is never copied.

Requesting `https://<viewer>/s/<link-token>/<filename>` makes the viewer read
the private link record, check the expiry and stream the object back with the
recorded content type.

Direct S3 presigned URLs are not used: SigV4 caps them at seven days, and a
30-day link that can be renewed in place needs a record the tool controls.

## Install

Download a release archive for your platform from the
[releases page](https://github.com/patriksimms/dkwws/releases) and put `dkwws`
on your `PATH`. Builds are published for Linux amd64, Linux arm64 and macOS
arm64.

To build from source with Go 1.27 or newer:

```console
$ go build ./cmd/dkwws
```

## Configure the CLI

Configuration comes from the environment, falling back to a `KEY=value` file at
`${XDG_CONFIG_HOME:-~/.config}/dkwws/config`. The environment always wins,
which keeps CI and `direnv` working without touching the file.

| Setting | Meaning |
| --- | --- |
| `DKWWS_S3_ENDPOINT` | HTTPS endpoint of the backend |
| `DKWWS_S3_REGION` | signing region, default `us-east-1` |
| `DKWWS_S3_BUCKET` | the private bucket |
| `DKWWS_S3_ACCESS_KEY_ID` | this machine's access key |
| `DKWWS_S3_SECRET_ACCESS_KEY` | this machine's secret key |
| `DKWWS_S3_PATH_STYLE` | path-style addressing, default `true` |
| `DKWWS_VIEWER_BASE_URL` | public origin of the viewer |

The configuration file holds long-lived credentials, so `dkwws` refuses to read
it unless it is mode `0600`:

```console
$ mkdir -p ~/.config/dkwws
$ install -m 600 /dev/null ~/.config/dkwws/config
$ cat > ~/.config/dkwws/config <<'EOF'
DKWWS_S3_ENDPOINT=https://s3.psimms.de
DKWWS_S3_BUCKET=dkwws
DKWWS_S3_ACCESS_KEY_ID=...
DKWWS_S3_SECRET_ACCESS_KEY=...
DKWWS_VIEWER_BASE_URL=https://dkwws.psimms.de
EOF
```

Credentials belong on the machine, never in this repository.

An endpoint on `http://` is refused for any non-loopback host, because the
secret key would go over the wire in the clear. Set
`DKWWS_S3_ALLOW_INSECURE=true` if you really do terminate TLS elsewhere.

## Use it

```console
$ dkwws upload plan.html                 # print the share URL
$ dkwws upload -json plan.html           # url, object, expiry, size, sha256
$ dkwws renew https://dkwws.../plan.html # fresh token and expiry, same file
$ dkwws renew ycdpbvvbdtentlk57ixealxnhwixeg6h # a bare token works too
```

`upload` refuses files over 25 MiB; pass `-max-size` in bytes to override.
`.html` and `.htm` are served as `text/html; charset=utf-8` so they render in
the browser; anything else is guessed from the extension and can be overridden
with `-content-type`.

Renewing an expired link is the normal case — the record is read regardless of
its expiry, and the old link stays dead.

## Deploy the viewer

The viewer is a single static binary that reads the same `DKWWS_S3_*` settings
plus `DKWWS_LISTEN_ADDR` (default `:8080`). It does not need
`DKWWS_VIEWER_BASE_URL`. `GET /healthz` returns `ok`.

The repository root builds a distroless image:

```console
$ docker build -t dkwws-viewer .
$ docker run --rm -p 8080:8080 --env-file viewer.env dkwws-viewer
```

On Coolify, create an application from this repository with the Dockerfile
build pack, set the domain, expose port 8080, point the health check at
`/healthz` and set the `DKWWS_S3_*` variables to the **read-only** key pair.

Nothing else is needed: no database, no volume, no background job.

## Credentials

Two scopes, provisioned however your backend does it. Written as an AWS-style
policy, an uploader may write objects and link records and read link records
back so it can renew them:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "s3:PutObject",
      "Resource": [
        "arn:aws:s3:::dkwws/objects/*",
        "arn:aws:s3:::dkwws/links/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::dkwws/links/*"
    }
  ]
}
```

The viewer only reads:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": [
        "arn:aws:s3:::dkwws/objects/*",
        "arn:aws:s3:::dkwws/links/*"
      ]
    }
  ]
}
```

Neither may list the bucket, delete anything, change policies, call
administrative APIs or touch another bucket. Give every machine its own key
pair so it can be revoked on its own. `dkwws renew` deliberately never reads
`objects/`, so the uploader policy above is genuinely sufficient.

Keep the bucket itself private. A public-read bucket policy would hand out
every object regardless of link expiry.

## S3 compatibility

dkwws depends on Signature Version 4 and three data operations:

- `PutObject`, including conditional creation with `If-None-Match: *`
- `GetObject`, streaming, with content type and length
- `HeadObject`

plus a configurable endpoint, region, bucket and path-style addressing. No
provider-specific administration API is used. The flexible-checksum trailers
newer AWS SDKs add by default are switched off, because many S3-compatible
backends reject them; dkwws records its own SHA-256 in the link record instead.

`If-None-Match: *` is what makes two uploads of the same filename safe: every
upload picks a fresh random object id and creates it conditionally, so an
upload can never overwrite an existing object. A backend that ignores the
header would break that guarantee, which is why it is part of the contract
rather than an optimisation.

RustFS is the first integration target. AWS S3, MinIO, Garage and Cloudflare R2
should satisfy the contract but are only supported once they have been run
against the integration tests.

## What the link does and does not protect

A link is a bearer token: anyone holding the URL can read the file until it
expires. There are no accounts and no per-recipient access.

The viewer sends `X-Robots-Tag: noindex, nofollow`, serves a `robots.txt` that
disallows everything, marks responses `private`, and never lets a cache outlive
the link's own expiry. Rejected tokens all produce the same 404 whether they
are malformed, unknown or simply not yours, so the viewer cannot be used to
probe the bucket. Tokens are truncated in the viewer's logs, because a full
token in a log line is a working link.

Uploaded HTML runs in the viewer's origin. The viewer sets no cookies and has
no authenticated surface, so there is nothing there to steal — but do not host
the viewer on a domain that shares cookies with something that does.

## Development

```console
$ go test ./...
$ go vet ./...
$ gofmt -l .
```

The tests run the whole delivery path in-process against
`internal/s3fake`, an S3-compatible server that verifies every SigV4 signature
with an independent implementation and enforces the same prefix-scoped
credential policies documented above. No container runtime is required.

## Scope

Single self-contained files only. No permanent links, no bucket browser, no
delete command, no multi-file sites. Uploaded objects are kept indefinitely;
only the links expire.
