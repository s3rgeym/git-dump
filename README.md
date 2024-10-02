## Git Dump

Dumps Exposed Git Folders.

```bash
go run ./cmd/git-dump -i urls.txt -o git_dumps -log debug -ua "my-custom-agent" -connect-timeout 5s -header-timeout 5s -keepalive-timeout 30s -request-timeout 30s -retries 3 -w 20 -max-errors 30 -max-rps 50
```
