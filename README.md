# TikTocker  

Automatically backup Mikrotik config to S3

## Setup

Create dedicated Mikrotik user with separate group.  
The group must have all permissions except for `telnet, romon`

Use the user in `config.yaml` file's `mikrotiks[].username` and `mikrotiks[].password`.

### Running locally
Create `.local/config.yaml` file with the following content:

Using S3
```yaml
log:
  level: info

s3:
  host: "http://host.s3"
  accessKey: "somekey"
  secretKey: "somesecret"
  region: "someregion"
  path: "bucket/path"
  usePathStyle: true

mikrotiks:
  - host: "192.168.88.1"
    username: "backupuser"
    password: "abcdefgh"
    encryptionKey: "somekey"
    metadata:
      automated: "true"
  - host: "192.168.88.2"
    username: "backupuser"
    password: "abcdefgh"
    encryptionKey: "somekey"
    metadata:
      automated: "true"
```

Download locally (testing)
```yaml
log:
  level: info

directory: "/tmp"

mikrotiks:
  - host: "192.168.88.1"
    username: "backupuser"
    password: "abcdefgh"
    encryptionKey: "somekey"
    metadata:
      automated: "true"
  - host: "192.168.88.2"
    username: "backupuser"
    password: "abcdefgh"
    encryptionKey: "somekey"
    metadata:
      automated: "true"
```

### Running in Kubernetes

TODO