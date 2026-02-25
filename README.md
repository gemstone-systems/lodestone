# Lodestone

Lodestone is a lightweight AT-URI resolver. Given one or more AT-URIs, it resolves the authority to a DID, looks up the appropriate PDS via the DID document, and returns the underlying record(s).

A public instance is available at `https://lodestone.gmstn.systems`.

## Usage

Resolve AT-URIs by querying the `/resolve` endpoint.

### Query parameters

| Parameter | Type                  | Description                                                                                                                                      |
| --------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `uris`    | `string` (repeatable) | One or more AT-URIs to resolve. Returns an ordered array of records.                                                                             |
| `uri`     | `string`              | ⚠️ Deprecated. A single AT-URI to resolve. Returns the record object directly rather than a wrapped array. Retained for backwards compatibility. |

Both handles (`alice.bsky.social`) and DIDs (`did:plc:...`) are accepted as the authority component of an AT-URI.

### Batch resolution

Pass multiple `uris` parameters to resolve several AT-URIs in a single request. Results are returned as an ordered array matching the position of each input URI. If a particular URI fails to resolve, its position in the array will contain an empty object (`{}`) rather than failing the entire request.

```
GET /resolve?uris=at://alice.bsky.social/app.bsky.feed.post/abc123&uris=at://bob.bsky.social/app.bsky.feed.post/xyz789
```

```json
[
  {
    "uri": "at://did:plc:abc/app.bsky.feed.post/abc123",
    "cid": "bafyre...",
    "value": { ... }
  },
  {
    "uri": "at://did:plc:xyz/app.bsky.feed.post/xyz789",
    "cid": "bafyre...",
    "value": { ... }
  }
]
```

### Single resolution (deprecated)

```
GET /resolve?uri=at://alice.bsky.social/app.bsky.feed.post/abc123
```

```json
{
  "uri": "at://did:plc:abc/app.bsky.feed.post/abc123",
  "cid": "bafyre...",
  "value": { ... }
}
```

### Partial authority and collection URIs

Lodestone also handles AT-URIs that resolve to a repo or collection rather than a specific record.

| URI form                                           | Resolves via                    |
| -------------------------------------------------- | ------------------------------- |
| `at://alice.bsky.social`                           | `com.atproto.repo.describeRepo` |
| `at://alice.bsky.social/app.bsky.feed.post`        | `com.atproto.repo.listRecords`  |
| `at://alice.bsky.social/app.bsky.feed.post/abc123` | `com.atproto.repo.getRecord`    |

### Example (TypeScript)

```ts
const params = new URLSearchParams();
params.append("uris", "at://alice.bsky.social/app.bsky.feed.post/abc123");
params.append("uris", "at://bob.bsky.social/app.bsky.feed.post/xyz789");

const res = await fetch(`https://lodestone.gmstn.systems/resolve?${params}`);
const records = await res.json();
```

### Lexicon

A Lexicon definition for this endpoint is available at [`lexicons/dev/sylfr/lodestone/resolve.json`](lexicons/dev/sylfr/lodestone/resolve.json) under the NSID `dev.sylfr.lodestone.resolve`.

---

## Hosting

### Prerequisites

- [Go](https://go.dev/dl/) 1.21 or later
- A domain pointing to your server
- `caddy` or `nginx` for TLS termination

### Building the binary

Clone the repository and build the binary with `go build`. Use the `-o` flag to place the output wherever you like:

```bash
git clone https://github.com/yourname/lodestone
cd lodestone
go mod tidy
go build -o /usr/local/bin/lodestone .
```

Verify the build:

```bash
lodestone --version
```

By default, Lodestone listens on port `8080`. TLS termination is expected to be handled by your reverse proxy.

### Systemd service

Create a service file at `/etc/systemd/system/lodestone.service`:

```ini
[Unit]
Description=Lodestone AT-URI Resolver
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/lodestone
Restart=on-failure
RestartSec=5s

# Harden the service
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

Then enable and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now lodestone
```

Check that it's running:

```bash
sudo systemctl status lodestone
```

### Caddy

Add the following to your `Caddyfile`. Caddy will handle TLS automatically via Let's Encrypt:

```caddy
lodestone.example.com {
    reverse_proxy localhost:8080
}
```

Reload Caddy to apply:

```bash
sudo systemctl reload caddy
```

### Nginx

Create a config file at `/etc/nginx/sites-available/lodestone`:

```nginx
server {
    listen 80;
    server_name lodestone.example.com;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable the site and obtain a TLS certificate via Certbot:

```bash
sudo ln -s /etc/nginx/sites-available/lodestone /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d lodestone.example.com
```

---

## Caching

Lodestone uses two in-memory LRU caches to avoid redundant network calls on repeated resolutions. Both caches are populated lazily on first access and expired entries are replaced on next use.

### DID cache

Resolved DID documents are cached separately from XRPC responses, since they are small, cheap to store, and rarely change. The DID cache holds up to **4,096 entries** with a TTL of **12 hours**.

### XRPC response cache

PDS responses are cached per full request URL. Different endpoint types have different TTLs reflecting how frequently their data is likely to change:

| Endpoint                        | TTL        | Notes                              |
| ------------------------------- | ---------- | ---------------------------------- |
| `com.atproto.repo.describeRepo` | 30 minutes | Repo metadata changes infrequently |
| `com.atproto.repo.getRecord`    | 2 minutes  | Records are mutable, kept short    |
| `com.atproto.repo.listRecords`  | None       | Not cached — goes stale too easily |

The XRPC cache holds up to **8,192 entries**. TTLs and cache sizes are defined as constants in `main.go` and can be adjusted to suit your use case.

### Limitations

Both caches are in-process and not shared across instances. If you are running multiple instances behind a load balancer, each will maintain its own independent cache. For multi-instance deployments, consider replacing the XRPC cache with an external store such as Redis or DynamoDB.
