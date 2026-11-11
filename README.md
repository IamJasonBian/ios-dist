# ios-dist

Distribution tooling for server to cluster to build ota

```
BUILD-COMPUTE ──build + rsync──► CLUSTER  (Go server, :8443/ota)
   │                                ▲      │
   │ Taildrop link                  │ ACK  │ page + app.ipa
   ▼                                │      ▼
 IPHONE ────────────────────────────┴──────┘
```

Using locally hosted cluster (A30 <-> Mac studio)

  Stand MacBooks for BUILD-COMPUTE, we can move to hosted Sequoia image distributions later

https://great-lakes.tail70b2c1.ts.net:8443/ota/
